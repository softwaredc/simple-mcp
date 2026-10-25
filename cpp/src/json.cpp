#include "mcp/json.hpp"

#include <cerrno>
#include <cmath>
#include <cstdio>
#include <cstdlib>
#include <cstring>

namespace mcp {
namespace json {

// ------------------------------------------------------------------ accessors

bool Value::as_bool() const {
    if (kind_ != Kind::Bool) throw TypeError("value is not a bool");
    return bool_;
}

double Value::as_number() const {
    if (kind_ != Kind::Number) throw TypeError("value is not a number");
    errno = 0;
    char* end = nullptr;
    double d = std::strtod(str_.c_str(), &end);
    if (errno == ERANGE) throw TypeError("number out of range: " + str_);
    return d;
}

const std::string& Value::as_number_literal() const {
    if (kind_ != Kind::Number) throw TypeError("value is not a number");
    return str_;
}

const std::string& Value::as_string() const {
    if (kind_ != Kind::String) throw TypeError("value is not a string");
    return str_;
}

const std::vector<Value>& Value::as_array() const {
    if (kind_ != Kind::Array) throw TypeError("value is not an array");
    return arr_;
}

std::vector<Value>& Value::as_array() {
    if (kind_ != Kind::Array) throw TypeError("value is not an array");
    return arr_;
}

const Value* Value::find(const std::string& key) const {
    if (kind_ != Kind::Object) return nullptr;
    for (const auto& kv : obj_) {
        if (kv.first == key) return &kv.second;
    }
    return nullptr;
}

Value& Value::operator[](const std::string& key) {
    if (kind_ != Kind::Object) throw TypeError("value is not an object");
    for (auto& kv : obj_) {
        if (kv.first == key) return kv.second;
    }
    obj_.emplace_back(key, Value());
    return obj_.back().second;
}

void Value::set(const std::string& key, Value v) {
    if (kind_ != Kind::Object) throw TypeError("value is not an object");
    for (auto& kv : obj_) {
        if (kv.first == key) {
            kv.second = std::move(v);
            return;
        }
    }
    obj_.emplace_back(key, std::move(v));
}

// ------------------------------------------------------------------ serializer

std::string escape_string(const std::string& s) {
    std::string out;
    out.reserve(s.size() + 2);
    out.push_back('"');
    for (unsigned char c : s) {
        switch (c) {
        case '"':  out += "\\\""; break;
        case '\\': out += "\\\\"; break;
        case '\b': out += "\\b";  break;
        case '\f': out += "\\f";  break;
        case '\n': out += "\\n";  break;
        case '\r': out += "\\r";  break;
        case '\t': out += "\\t";  break;
        default:
            if (c < 0x20) {
                // Every other control character must be escaped as \u00XX.
                char buf[8];
                std::snprintf(buf, sizeof buf, "\\u%04x", c);
                out += buf;
            } else {
                out.push_back(static_cast<char>(c));
            }
        }
    }
    out.push_back('"');
    return out;
}

void Value::dump_to(std::string& out) const {
    switch (kind_) {
    case Kind::Null:   out += "null"; break;
    case Kind::Bool:   out += bool_ ? "true" : "false"; break;
    case Kind::Number: out += str_; break; // literal, verbatim
    case Kind::String: out += escape_string(str_); break;
    case Kind::Array: {
        out.push_back('[');
        for (size_t i = 0; i < arr_.size(); ++i) {
            if (i) out.push_back(',');
            arr_[i].dump_to(out);
        }
        out.push_back(']');
        break;
    }
    case Kind::Object: {
        out.push_back('{');
        for (size_t i = 0; i < obj_.size(); ++i) {
            if (i) out.push_back(',');
            out += escape_string(obj_[i].first);
            out.push_back(':');
            obj_[i].second.dump_to(out);
        }
        out.push_back('}');
        break;
    }
    }
}

std::string Value::dump() const {
    std::string out;
    out.reserve(64);
    dump_to(out);
    return out;
}

// --------------------------------------------------------------------- parser

namespace {

// Parser is a straightforward recursive descent over a string. It keeps the
// input and a cursor; every function assumes leading whitespace has been
// skipped by its caller.
class Parser {
public:
    explicit Parser(const std::string& text) : s_(text) {}

    Value parse_document() {
        skip_ws();
        Value v = parse_value();
        skip_ws();
        if (pos_ != s_.size()) {
            fail("trailing content after JSON value");
        }
        return v;
    }

private:
    [[noreturn]] void fail(const std::string& why) const {
        char buf[64];
        std::snprintf(buf, sizeof buf, " at byte %zu", pos_);
        throw ParseError("json: " + why + buf);
    }

    void skip_ws() {
        while (pos_ < s_.size()) {
            char c = s_[pos_];
            if (c == ' ' || c == '\t' || c == '\n' || c == '\r') {
                ++pos_;
            } else {
                break;
            }
        }
    }

    char peek() const {
        if (pos_ >= s_.size()) fail("unexpected end of input");
        return s_[pos_];
    }

    void expect(char c) {
        if (pos_ >= s_.size() || s_[pos_] != c) {
            fail(std::string("expected '") + c + "'");
        }
        ++pos_;
    }

    bool literal(const char* word) {
        size_t n = std::strlen(word);
        if (s_.compare(pos_, n, word) == 0) {
            pos_ += n;
            return true;
        }
        return false;
    }

    // kMaxDepth is what stands between a hostile document and the call stack.
    //
    // Recursive descent spends stack per nesting level, so "[[[[...]]]]" a
    // hundred thousand deep overflows it and takes the process down with a
    // segfault rather than an error anyone can catch. Go's encoding/json caps
    // nesting at 10000 for exactly this reason, and at ~200 bytes of nesting
    // per level that overflows an 8 MiB stack long before 10000. 100 is
    // generous for MCP, whose deepest legitimate structure is a tool result
    // nested half a dozen deep.
    static constexpr int kMaxDepth = 100;

    Value parse_value() {
        skip_ws();
        char c = peek();

        // Counted on the way in and unwound by the destructor on the way out,
        // so the early returns above cannot leak a level.
        ++depth_;
        struct Pop {
            int& d;
            ~Pop() { --d; }
        } pop{depth_};
        if (depth_ > kMaxDepth) fail("nesting too deep");

        switch (c) {
        case 'n': if (literal("null")) return Value::null(); fail("bad literal");
        case 't': if (literal("true")) return Value::boolean(true); fail("bad literal");
        case 'f': if (literal("false")) return Value::boolean(false); fail("bad literal");
        case '"': return Value::string(parse_string());
        case '[': return parse_array();
        case '{': return parse_object();
        default:
            if (c == '-' || (c >= '0' && c <= '9')) return parse_number();
            fail("unexpected character");
        }
    }

    // parse_number captures the literal text rather than converting, so the
    // value survives a round trip unchanged. Conversion happens lazily in
    // as_number(), only when arithmetic is actually needed.
    Value parse_number() {
        size_t start = pos_;
        if (peek() == '-') ++pos_;
        if (pos_ >= s_.size()) fail("truncated number");
        if (s_[pos_] == '0') {
            ++pos_;
        } else if (s_[pos_] >= '1' && s_[pos_] <= '9') {
            while (pos_ < s_.size() && s_[pos_] >= '0' && s_[pos_] <= '9') ++pos_;
        } else {
            fail("invalid number");
        }
        if (pos_ < s_.size() && s_[pos_] == '.') {
            ++pos_;
            if (pos_ >= s_.size() || s_[pos_] < '0' || s_[pos_] > '9') {
                fail("digit expected after decimal point");
            }
            while (pos_ < s_.size() && s_[pos_] >= '0' && s_[pos_] <= '9') ++pos_;
        }
        if (pos_ < s_.size() && (s_[pos_] == 'e' || s_[pos_] == 'E')) {
            ++pos_;
            if (pos_ < s_.size() && (s_[pos_] == '+' || s_[pos_] == '-')) ++pos_;
            if (pos_ >= s_.size() || s_[pos_] < '0' || s_[pos_] > '9') {
                fail("digit expected in exponent");
            }
            while (pos_ < s_.size() && s_[pos_] >= '0' && s_[pos_] <= '9') ++pos_;
        }
        return Value::number(s_.substr(start, pos_ - start));
    }

    std::string parse_string() {
        expect('"');
        std::string out;
        while (true) {
            if (pos_ >= s_.size()) fail("unterminated string");
            unsigned char c = static_cast<unsigned char>(s_[pos_++]);
            if (c == '"') return out;
            if (c < 0x20) fail("control character in string");
            if (c != '\\') {
                out.push_back(static_cast<char>(c));
                continue;
            }
            // Escape sequence.
            if (pos_ >= s_.size()) fail("unterminated escape");
            char e = s_[pos_++];
            switch (e) {
            case '"':  out.push_back('"');  break;
            case '\\': out.push_back('\\'); break;
            case '/':  out.push_back('/');  break;
            case 'b':  out.push_back('\b'); break;
            case 'f':  out.push_back('\f'); break;
            case 'n':  out.push_back('\n'); break;
            case 'r':  out.push_back('\r'); break;
            case 't':  out.push_back('\t'); break;
            case 'u': {
                uint32_t cp = parse_hex4();
                // Surrogate pairs: a high surrogate followed by a low one
                // encodes a single code point above U+FFFF. Handling only half
                // of the pair produces the replacement character, which is how
                // most hand-rolled parsers get emoji wrong.
                if (cp >= 0xD800 && cp <= 0xDBFF) {
                    if (pos_ + 1 < s_.size() && s_[pos_] == '\\' && s_[pos_ + 1] == 'u') {
                        pos_ += 2;
                        uint32_t lo = parse_hex4();
                        if (lo < 0xDC00 || lo > 0xDFFF) {
                            fail("invalid low surrogate");
                        }
                        cp = 0x10000 + ((cp - 0xD800) << 10) + (lo - 0xDC00);
                    } else {
                        // An unpaired high surrogate is not valid JSON. Emit
                        // the replacement character rather than throwing, so
                        // one bad escape does not kill the whole message.
                        append_utf8(out, 0xFFFD);
                        break;
                    }
                } else if (cp >= 0xDC00 && cp <= 0xDFFF) {
                    append_utf8(out, 0xFFFD);
                    break;
                }
                append_utf8(out, cp);
                break;
            }
            default: fail("unknown escape");
            }
        }
    }

    uint32_t parse_hex4() {
        if (pos_ + 4 > s_.size()) fail("truncated \\u escape");
        uint32_t v = 0;
        for (int i = 0; i < 4; ++i) {
            char c = s_[pos_++];
            v <<= 4;
            if (c >= '0' && c <= '9') v |= static_cast<uint32_t>(c - '0');
            else if (c >= 'a' && c <= 'f') v |= static_cast<uint32_t>(c - 'a' + 10);
            else if (c >= 'A' && c <= 'F') v |= static_cast<uint32_t>(c - 'A' + 10);
            else fail("bad hex digit in \\u escape");
        }
        return v;
    }

    // append_utf8 encodes one code point as UTF-8. A member function rather
    // than a free one because it reports malformed input through fail().
    void append_utf8(std::string& out, uint32_t cp) {
        if (cp > 0x10FFFF || (cp >= 0xD800 && cp <= 0xDFFF)) {
            fail("code point out of range");
        }
        if (cp < 0x80) {
            out.push_back(static_cast<char>(cp));
        } else if (cp < 0x800) {
            out.push_back(static_cast<char>(0xC0 | (cp >> 6)));
            out.push_back(static_cast<char>(0x80 | (cp & 0x3F)));
        } else if (cp < 0x10000) {
            out.push_back(static_cast<char>(0xE0 | (cp >> 12)));
            out.push_back(static_cast<char>(0x80 | ((cp >> 6) & 0x3F)));
            out.push_back(static_cast<char>(0x80 | (cp & 0x3F)));
        } else {
            out.push_back(static_cast<char>(0xF0 | (cp >> 18)));
            out.push_back(static_cast<char>(0x80 | ((cp >> 12) & 0x3F)));
            out.push_back(static_cast<char>(0x80 | ((cp >> 6) & 0x3F)));
            out.push_back(static_cast<char>(0x80 | (cp & 0x3F)));
        }
    }

    Value parse_array() {
        expect('[');
        Value v = Value::array();
        skip_ws();
        if (peek() == ']') {
            ++pos_;
            return v;
        }
        while (true) {
            v.as_array().push_back(parse_value());
            skip_ws();
            char c = peek();
            if (c == ']') {
                ++pos_;
                return v;
            }
            if (c != ',') fail("expected ',' or ']' in array");
            ++pos_;
        }
    }

    Value parse_object() {
        expect('{');
        Value v = Value::object();
        skip_ws();
        if (peek() == '}') {
            ++pos_;
            return v;
        }
        while (true) {
            skip_ws();
            std::string key = parse_string();
            skip_ws();
            expect(':');
            v.set(key, parse_value());
            skip_ws();
            char c = peek();
            if (c == '}') {
                ++pos_;
                return v;
            }
            if (c != ',') fail("expected ',' or '}' in object");
            ++pos_;
        }
    }

    const std::string& s_;
    size_t pos_ = 0;
    int depth_ = 0;
};

} // namespace

Value Value::parse(const std::string& text) {
    Parser p(text);
    return p.parse_document();
}

} // namespace json
} // namespace mcp

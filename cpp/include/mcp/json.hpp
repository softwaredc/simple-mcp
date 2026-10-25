// minimal JSON for MCP. Zero dependencies, C++17.
//
// Why hand-roll this when every C++ project has nlohmann/json available?
// Three reasons, in order of importance:
//
//  1. This is a teaching repository. A JSON parser you can read in one sitting
//     is worth more here than one you can trust in production.
//
//  2. MCP messages are small and simple. No floats beyond a handful of digits,
//     no deep nesting, no exotic escapes in practice.
//
//  3. It lets us make one design decision that an off-the-shelf library makes
//     for us and that matters for MCP specifically: numbers keep their literal
//     text. See as_number_literal() below.
//
// The parser is recursive descent over the whole input, which is fine for
// messages measured in kilobytes and would not be fine for a hundred-megabyte
// document.
#ifndef MCP_JSON_HPP
#define MCP_JSON_HPP

#include <cstdint>
#include <stdexcept>
#include <string>
#include <utility>
#include <vector>

namespace mcp {
namespace json {

// ParseError is thrown for malformed input. The message includes the byte
// offset, because "invalid JSON" without a position is useless when debugging
// a protocol conversation.
struct ParseError : std::runtime_error {
    explicit ParseError(const std::string& msg) : std::runtime_error(msg) {}
};

// Value is one JSON node.
//
// Objects are a vector of pairs rather than a map. Two reasons: insertion order
// is preserved, so re-serialising a parsed object reproduces the input (which
// makes tests readable), and MCP objects are small enough that linear lookup
// beats hashing.
class Value {
public:
    enum class Kind { Null, Bool, Number, String, Array, Object };

    Value() : kind_(Kind::Null) {}

    // Factory functions. Named rather than overloaded so call sites read
    // clearly: Value::string("x") versus Value(static_cast<const char*>("x")).
    static Value null() { return Value(); }
    static Value boolean(bool b) {
        Value v;
        v.kind_ = Kind::Bool;
        v.bool_ = b;
        return v;
    }
    static Value number(const std::string& literal) {
        Value v;
        v.kind_ = Kind::Number;
        v.str_ = literal; // kept verbatim, see as_number_literal()
        return v;
    }
    static Value string(std::string s) {
        Value v;
        v.kind_ = Kind::String;
        v.str_ = std::move(s);
        return v;
    }
    static Value array(std::vector<Value> items = {}) {
        Value v;
        v.kind_ = Kind::Array;
        v.arr_ = std::move(items);
        return v;
    }
    static Value object() {
        Value v;
        v.kind_ = Kind::Object;
        return v;
    }

    Kind kind() const { return kind_; }
    bool is_null() const { return kind_ == Kind::Null; }
    bool is_bool() const { return kind_ == Kind::Bool; }
    bool is_number() const { return kind_ == Kind::Number; }
    bool is_string() const { return kind_ == Kind::String; }
    bool is_array() const { return kind_ == Kind::Array; }
    bool is_object() const { return kind_ == Kind::Object; }

    // Accessors. Calling the wrong one for the kind is a programming error and
    // throws TypeError rather than returning garbage.
    bool as_bool() const;
    double as_number() const;           // parsed from the literal, for arithmetic
    const std::string& as_string() const;
    const std::vector<Value>& as_array() const;
    std::vector<Value>& as_array();

    // as_number_literal returns the exact text the parser saw. This is what
    // makes round-tripping lossless: an id of 1 stays "1" instead of becoming
    // "1.0" or "1e+00", which is the difference between a client matching
    // responses correctly and hanging forever.
    const std::string& as_number_literal() const;

    // Object access. find returns nullptr when the key is absent, which is the
    // common case for optional fields and should not be an error.
    const Value* find(const std::string& key) const;
    Value& operator[](const std::string& key);
    void set(const std::string& key, Value v);
    bool has(const std::string& key) const { return find(key) != nullptr; }
    const std::vector<std::pair<std::string, Value>>& members() const {
        return obj_;
    }

    // dump serialises to compact JSON (no whitespace), which is what MCP wants
    // on the wire.
    std::string dump() const;

    // parse reads one complete JSON value from text. Trailing content after
    // the value is an error: a transport delivers exactly one message per
    // frame, so a second value means the framing is broken.
    static Value parse(const std::string& text);

private:
    void dump_to(std::string& out) const;

    Kind kind_;
    bool bool_ = false;
    std::string str_; // string payload, or number literal
    std::vector<Value> arr_;
    std::vector<std::pair<std::string, Value>> obj_;
};

// TypeError is thrown when an accessor is used on the wrong kind.
struct TypeError : std::runtime_error {
    explicit TypeError(const std::string& msg) : std::runtime_error(msg) {}
};

// escape_string produces a JSON string literal, escaping everything the spec
// requires: quote, backslash, and control characters below 0x20.
std::string escape_string(const std::string& s);

} // namespace json
} // namespace mcp

#endif // MCP_JSON_HPP

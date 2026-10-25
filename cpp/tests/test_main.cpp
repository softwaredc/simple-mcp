// Test suite for the C++ implementation.
//
// A hand-rolled assertion macro rather than a framework, on purpose: the
// project has no dependencies, and pulling in gtest to test 2,000 lines of
// teaching code would be the wrong trade. Output is TAP-ish so a human can read
// it and a script can count it.
#include <cstdio>
#include <iostream>
#include <sstream>
#include <string>

#include "mcp/client.hpp"
#include "mcp/json.hpp"
#include "mcp/jsonrpc.hpp"
#include "mcp/server.hpp"
#include "mcp/types.hpp"

using namespace mcp;

static int g_checks = 0;
static int g_failures = 0;

#define CHECK(cond, what)                                                      \
    do {                                                                       \
        ++g_checks;                                                            \
        if (!(cond)) {                                                         \
            ++g_failures;                                                      \
            std::fprintf(stderr, "FAIL %s:%d: %s\n", __FILE__, __LINE__, what);\
        }                                                                      \
    } while (0)

#define CHECK_EQ(a, b, what)                                                   \
    do {                                                                       \
        ++g_checks;                                                            \
        auto _va = (a);                                                        \
        auto _vb = (b);                                                        \
        if (!(_va == _vb)) {                                                   \
            ++g_failures;                                                      \
            std::fprintf(stderr, "FAIL %s:%d: %s (%s != %s)\n", __FILE__,      \
                         __LINE__, what, std::to_string(_va).c_str(),          \
                         std::to_string(_vb).c_str());                         \
        }                                                                      \
    } while (0)

// ----------------------------------------------------------------- json tests

static void test_json_parse_and_dump() {
    json::Value v = json::Value::parse(R"({"a":1,"b":"x","c":[true,null],"d":{"e":-2.5}})");
    CHECK(v.is_object(), "parses an object");
    CHECK(v.find("a")->is_number(), "a is a number");
    CHECK(v.find("b")->as_string() == "x", "b round-trips");
    CHECK(v.find("c")->is_array() && v.find("c")->as_array().size() == 2, "array of two");
    CHECK(v.find("c")->as_array()[0].as_bool() == true, "first element is true");
    CHECK(v.find("c")->as_array()[1].is_null(), "second element is null");
    CHECK(v.find("d")->find("e")->as_number() == -2.5, "nested negative decimal");

    std::string dumped = v.dump();
    // Insertion order is preserved, so the output equals the input.
    CHECK(dumped == R"({"a":1,"b":"x","c":[true,null],"d":{"e":-2.5}})",
          "dump preserves key order and literals");
}

static void test_number_literal_preserved() {
    // The reason numbers keep their literal text: an id of 1 must not come
    // back as 1.0, or a client matching responses by id breaks.
    json::Value v = json::Value::parse(R"({"id":1})");
    CHECK(v.find("id")->as_number_literal() == "1", "integer literal stays 1");

    json::Value f = json::Value::parse(R"({"id":1.0})");
    CHECK(f.find("id")->as_number_literal() == "1.0", "1.0 stays 1.0");

    json::Value big = json::Value::parse(R"({"id":9007199254740993})");
    CHECK(big.find("id")->as_number_literal() == "9007199254740993",
          "beyond float64 precision the digits survive");
}

static void test_string_escapes() {
    json::Value v = json::Value::parse(R"({"s":"a\"b\\c\n\t\u0041"})");
    CHECK(v.find("s")->as_string() == "a\"b\\c\n\tA", "escapes decode");

    json::Value out = json::Value::object();
    out.set("s", json::Value::string(std::string("tab\there")));
    CHECK(out.dump() == R"({"s":"tab\there"})", "tab is escaped on the way out");
}

static void test_surrogate_pairs() {
    // U+1F600 (emoji) is a surrogate pair in \u form. Handling only half of it
    // is the classic hand-rolled-parser bug.
    json::Value v = json::Value::parse(R"({"e":"\uD83D\uDE00"})");
    const std::string& s = v.find("e")->as_string();
    CHECK(s.size() == 4, "emoji decodes to 4 UTF-8 bytes");
    CHECK(static_cast<unsigned char>(s[0]) == 0xF0, "leading byte is 0xF0");

    json::Value bad = json::Value::parse(R"({"e":"\uD83D"})");
    CHECK(bad.find("e")->as_string().size() == 3, "lone high surrogate becomes U+FFFD");
}

static void test_parse_errors_report_position() {
    bool threw = false;
    try {
        json::Value::parse(R"({"a": })");
    } catch (const json::ParseError& e) {
        threw = true;
        CHECK(std::string(e.what()).find("byte") != std::string::npos,
              "error names the byte offset");
    }
    CHECK(threw, "malformed JSON throws");

    threw = false;
    try {
        json::Value::parse(R"({"a":1} {"b":2})"); // two values, one frame
    } catch (const json::ParseError&) {
        threw = true;
    }
    CHECK(threw, "trailing content after a value is an error");
}

static void test_control_characters_escaped() {
    json::Value v = json::Value::object();
    v.set("c", json::Value::string(std::string(1, '\x01')));
    CHECK(v.dump() == R"({"c":"\u0001"})", "control characters are escaped");
}

// ------------------------------------------------------------- jsonrpc tests

static void test_request_parsing() {
    Request r = parse_request(R"({"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"x"}})");
    CHECK(r.has_id, "id present");
    CHECK(r.id_raw == "7", "numeric id kept as text");
    CHECK(r.method == "tools/call", "method decoded");
    CHECK(r.params_raw == R"({"name":"x"})", "params kept as raw JSON");

    Request n = parse_request(R"({"jsonrpc":"2.0","method":"notifications/initialized"})");
    CHECK(!n.has_id, "notification has no id");
    CHECK(n.is_notification(), "is_notification() is true");

    bool threw = false;
    try {
        parse_request(R"({"jsonrpc":"1.0","id":1,"method":"x"})");
    } catch (const json::ParseError&) {
        threw = true;
    }
    CHECK(threw, "wrong jsonrpc version rejected");
}

static void test_response_round_trip() {
    Response r = Response::build_result("42", R"({"ok":true})");
    std::string dumped = dump_response(r);
    CHECK(dumped.find(R"("id":42)") != std::string::npos, "id emitted as a number");
    CHECK(dumped.find(R"("result":{"ok":true})") != std::string::npos, "result emitted");

    Response e = Response::build_error("9", RpcError(err::kMethodNotFound, "nope"));
    std::string ed = dump_response(e);
    CHECK(ed.find(R"("code":-32601)") != std::string::npos, "error code emitted");
    CHECK(ed.find(R"("message":"nope")") != std::string::npos, "error message emitted");
    CHECK(ed.find("\"result\"") == std::string::npos, "no result alongside an error");
}

// -------------------------------------------------------------- server tests

static Server* build_server() {
    static Server* s = nullptr;
    if (s == nullptr) {
        s = new Server("test-server", "1.0.0");
        s->set_instructions("Test instructions.");
        s->add_tool("echo", "Repeat the text",
                    R"({"type":"object","properties":{"text":{"type":"string"}},"required":["text"]})",
                    [](const json::Value& args) -> std::vector<Content> {
                        const json::Value* t = args.find("text");
                        if (t == nullptr) throw ToolError("missing text");
                        return {Content::make_text(t->as_string())};
                    });
        s->add_tool("boom", "Always fails", R"({"type":"object","properties":{}})",
                    [](const json::Value&) -> std::vector<Content> {
                        throw ToolError("deliberate failure");
                    });
        s->add_resource("file:///note", "Note", "a note", "text/plain",
                        [] { return std::string("note body"); });
        s->add_prompt("greet", "Greet someone", {{"who", "who to greet", true}},
                      [](const std::map<std::string, std::string>& a) {
                          return std::vector<PromptMessage>{
                              PromptMessage{"user", Content::make_text("hello " + a.at("who"))}};
                      });
    }
    return s;
}

static std::string call_raw(Server& s, const std::string& raw) {
    auto out = s.handle_message(raw);
    return out.has_value() ? *out : std::string("<no response>");
}

static void test_handshake() {
    Server& s = *build_server();
    std::string out = call_raw(s, R"({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"c","version":"1"}}})");
    CHECK(out.find(R"("protocolVersion":"2025-06-18")") != std::string::npos,
          "client's version echoed back");
    CHECK(out.find(R"("serverInfo":{"name":"test-server","version":"1.0.0"})") != std::string::npos,
          "serverInfo reported");
    CHECK(out.find(R"("instructions":"Test instructions.")") != std::string::npos,
          "instructions reported");

    // notifications/initialized is a notification: no response.
    auto none = s.handle_message(R"({"jsonrpc":"2.0","method":"notifications/initialized"})");
    CHECK(!none.has_value(), "notification produces no response");
}

static void test_pre_handshake_refused() {
    Server fresh("fresh", "1.0.0");
    std::string out = call_raw(fresh, R"({"jsonrpc":"2.0","id":1,"method":"tools/list"})");
    CHECK(out.find("not initialized") != std::string::npos,
          "calls before initialize are refused");
}

static void test_tool_call() {
    Server& s = *build_server();
    call_raw(s, R"({"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"c","version":"1"}}})");

    std::string ok = call_raw(s, R"({"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hi"}}})");
    CHECK(ok.find(R"("text":"hi")") != std::string::npos, "echo returns the text");
    CHECK(ok.find("isError") == std::string::npos, "no isError on success");

    std::string bad = call_raw(s, R"({"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"text":42}}})");
    CHECK(bad.find(R"("code":-32602)") != std::string::npos, "wrong type is -32602");
    CHECK(bad.find("invalid arguments") != std::string::npos, "validation message present");

    std::string fail = call_raw(s, R"({"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"boom","arguments":{}}})");
    CHECK(fail.find(R"("isError":true)") != std::string::npos, "tool failure is isError");
    CHECK(fail.find(R"("code":)") == std::string::npos, "tool failure is not a protocol error");

    std::string unknown = call_raw(s, R"({"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"nope","arguments":{}}})");
    CHECK(unknown.find("unknown tool") != std::string::npos, "unknown tool reported");
}

static void test_resources_and_prompts() {
    Server& s = *build_server();
    call_raw(s, R"({"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"c","version":"1"}}})");

    std::string read = call_raw(s, R"({"jsonrpc":"2.0","id":6,"method":"resources/read","params":{"uri":"file:///note"}})");
    CHECK(read.find(R"("text":"note body")") != std::string::npos, "resource read");

    std::string missing = call_raw(s, R"({"jsonrpc":"2.0","id":7,"method":"resources/read","params":{"uri":"file:///nope"}})");
    CHECK(missing.find(R"("code":-32002)") != std::string::npos, "unknown URI is -32002");

    std::string prompt = call_raw(s, R"({"jsonrpc":"2.0","id":8,"method":"prompts/get","params":{"name":"greet","arguments":{"who":"world"}}})");
    CHECK(prompt.find("hello world") != std::string::npos, "prompt rendered");

    std::string noarg = call_raw(s, R"({"jsonrpc":"2.0","id":9,"method":"prompts/get","params":{"name":"greet","arguments":{}}})");
    CHECK(noarg.find("requires argument") != std::string::npos, "missing required argument reported");
}

static void test_parse_error_response() {
    Server& s = *build_server();
    std::string out = call_raw(s, "{not json");
    CHECK(out.find(R"("id":null)") != std::string::npos, "parse error carries id null");
    CHECK(out.find(R"("code":-32700)") != std::string::npos, "parse error is -32700");
}

// ------------------------------------------------- client/server integration

// A transport backed by in-memory buffers, so the client and server can talk
// without a real pipe.
//
// It blocks when empty, because a real socket blocks. An earlier version of
// this test returned EOF the moment the buffer ran dry, which made the
// client's read loop exit instantly and turned every pending call into a
// spurious "connection closed" error. Reproducing the blocking behaviour is
// what makes the test exercise the real code path.
class LoopbackTransport : public Transport {
public:
    std::optional<std::string> read_message() override {
        std::unique_lock<std::mutex> lock(mu_);
        cv_.wait(lock, [&] { return closed_ || pos_ < buf_.size(); });
        if (pos_ >= buf_.size()) return std::nullopt;
        size_t nl = buf_.find('\n', pos_);
        if (nl == std::string::npos) nl = buf_.size();
        std::string line = buf_.substr(pos_, nl - pos_);
        pos_ = nl + 1;
        return line;
    }

    void write_message(const std::string& msg) override {
        {
            std::lock_guard<std::mutex> lock(peer_->mu_);
            peer_->buf_.append(msg);
            peer_->buf_.push_back('\n');
        }
        peer_->cv_.notify_all();
    }

    void close() override {
        {
            std::lock_guard<std::mutex> lock(mu_);
            closed_ = true;
        }
        cv_.notify_all();
        if (peer_) {
            std::lock_guard<std::mutex> lock(peer_->mu_);
            peer_->closed_ = true;
            peer_->cv_.notify_all();
        }
    }

    LoopbackTransport* peer_ = nullptr;

private:
    std::mutex mu_;
    std::condition_variable cv_;
    std::string buf_;
    size_t pos_ = 0;
    bool closed_ = false;
};

static void test_client_server_loopback() {
    LoopbackTransport client_side, server_side;
    client_side.peer_ = &server_side;
    server_side.peer_ = &client_side;

    Server& s = *build_server();

    // The server runs in its own thread, exactly as it would in its own
    // process. Without this the client writes into a buffer nobody reads and
    // the read loop immediately sees EOF.
    std::thread server_thread([&] { s.serve(server_side); });

    Client c(client_side, "loopback-client", "1.0.0");

    c.connect();
    CHECK(c.server_info().name == "test-server", "client learned the server name");
    CHECK(c.protocol_version() == "2025-06-18", "client learned the protocol version");

    auto tools = c.list_tools();
    CHECK(tools.size() == 2, "client sees both tools");

    auto res = c.call_tool("echo", [] {
        json::Value a = json::Value::object();
        a.set("text", json::Value::string("over the wire"));
        return a;
    }());
    CHECK(!res.is_error, "successful call is not an error");
    CHECK(res.content.size() == 1 && res.content[0].text == "over the wire",
          "content came back intact");

    auto failed = c.call_tool("boom", json::Value::object());
    CHECK(failed.is_error, "tool failure arrives as isError");

    bool threw = false;
    try {
        json::Value a = json::Value::object();
        a.set("text", json::Value::number("42"));
        c.call_tool("echo", a);
    } catch (const RpcError& e) {
        threw = true;
        CHECK(e.code == err::kInvalidParams, "schema violation is a protocol error");
    }
    CHECK(threw, "schema violation throws on the client side");

    auto resources = c.list_resources();
    CHECK(resources.size() == 1 && resources[0].uri == "file:///note", "resources listed");
    auto contents = c.read_resource("file:///note");
    CHECK(contents.size() == 1 && contents[0].text == "note body", "resource read through client");

    auto msgs = c.get_prompt("greet", {{"who", "cpp"}});
    CHECK(msgs.size() == 1 && msgs[0].content.text == "hello cpp", "prompt rendered through client");

    c.ping(); // must not throw

    // Closing the transport makes the server's read loop finish, which lets
    // the thread join cleanly instead of leaking it at exit.
    client_side.close();
    server_thread.join();
}

// Deep nesting must be rejected, not crashed on.
//
// The parser recurses once per level, so without a depth cap a document of
// 100,000 opening brackets overflows the stack and kills the process -- not
// with an error anyone can catch, but with a segfault. Go's encoding/json caps
// nesting for exactly this reason.
static void test_nesting_depth_capped() {
    std::string deep;
    deep.reserve(200000);
    for (int i = 0; i < 100000; ++i) deep += '[';
    for (int i = 0; i < 100000; ++i) deep += ']';

    bool threw = false;
    try {
        json::Value::parse(deep);
    } catch (const json::ParseError&) {
        threw = true;
    }
    CHECK(threw, "deeply nested input must be rejected, not crash the process");

    // A hundred levels is well past anything MCP sends and still fine.
    std::string ok;
    for (int i = 0; i < 50; ++i) ok += '[';
    ok += "1";
    for (int i = 0; i < 50; ++i) ok += ']';
    bool parsed = true;
    try {
        json::Value::parse(ok);
    } catch (const json::ParseError&) {
        parsed = false;
    }
    CHECK(parsed, "50 levels of nesting must still parse");
}

// A tool whose arguments are a list relies on `items`. The validator declares
// it and used to never read it, which let any element through: the outer
// "array" check passes and nothing else applies to a list.
static void test_schema_items() {
    std::string err;
    json::Value schema = json::Value::parse(
        R"({"type":"object","properties":{"tags":{"type":"array","items":{"type":"string"}}}})");

    CHECK(validate_schema(schema, json::Value::parse(R"({"tags":["a","b"]})"), err),
          "string list validates");
    CHECK(validate_schema(schema, json::Value::parse(R"({"tags":[]})"), err),
          "empty list validates");
    CHECK(!validate_schema(schema, json::Value::parse(R"({"tags":["a",2]})"), err),
          "a non-string element is rejected");
    CHECK(err.find("[1]") != std::string::npos,
          "the error names which element was wrong");
}

// "type" as a union is how JSON Schema writes "string or null". Reading only
// the single-string form skips validation entirely for those tools.
static void test_schema_type_union() {
    std::string err;
    json::Value schema = json::Value::parse(
        R"({"type":"object","properties":{"n":{"type":["string","null"]}},"required":["n"]})");

    CHECK(validate_schema(schema, json::Value::parse(R"({"n":"x"})"), err),
          "a string satisfies the union");
    CHECK(validate_schema(schema, json::Value::parse(R"({"n":null})"), err),
          "null satisfies the union");
    CHECK(!validate_schema(schema, json::Value::parse(R"({"n":7})"), err),
          "a number satisfies neither");
}

int main() {
    std::printf("simple-mcp C++ test suite\n");

    test_json_parse_and_dump();
    test_number_literal_preserved();
    test_string_escapes();
    test_surrogate_pairs();
    test_parse_errors_report_position();
    test_control_characters_escaped();

    test_nesting_depth_capped();
    test_schema_items();
    test_schema_type_union();

    test_request_parsing();
    test_response_round_trip();

    test_handshake();
    test_pre_handshake_refused();
    test_tool_call();
    test_resources_and_prompts();
    test_parse_error_response();

    test_client_server_loopback();

    std::printf("%d checks, %d failures\n", g_checks, g_failures);
    return g_failures == 0 ? 0 : 1;
}

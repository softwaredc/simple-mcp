#include "mcp/jsonrpc.hpp"

namespace mcp {

namespace {

// looks_numeric reports whether text is a JSON number literal. Kept next to its
// only caller so the rule stays in one place.
bool looks_numeric(const std::string& s) {
    if (s.empty()) return false;
    for (char c : s) {
        if (!((c >= '0' && c <= '9') || c == '-' || c == '.' || c == 'e' || c == 'E' ||
              c == '+')) {
            return false;
        }
    }
    return true;
}

} // namespace

Request parse_request(const std::string& text) {
    json::Value v = json::Value::parse(text);
    if (!v.is_object()) {
        throw json::ParseError("jsonrpc: request must be an object");
    }

    Request r;

    const json::Value* jr = v.find("jsonrpc");
    if (jr == nullptr || !jr->is_string()) {
        throw json::ParseError("jsonrpc: missing jsonrpc version");
    }
    r.jsonrpc = jr->as_string();
    if (r.jsonrpc != "2.0") {
        throw json::ParseError("jsonrpc: unsupported version " + r.jsonrpc);
    }

    // The id is optional: its absence is what makes this a notification.
    // Note we keep the raw text, so a numeric id round-trips exactly.
    const json::Value* id = v.find("id");
    if (id != nullptr && !id->is_null()) {
        r.has_id = true;
        if (id->is_number()) {
            r.id_raw = id->as_number_literal();
        } else if (id->is_string()) {
            r.id_raw = id->as_string();
        } else {
            // Booleans and containers are legal JSON-RPC ids but nothing sane
            // uses them. Refuse rather than silently normalise, because the
            // response would then carry a different id than the request.
            throw json::ParseError("jsonrpc: id must be a string or number");
        }
    }

    const json::Value* m = v.find("method");
    if (m == nullptr || !m->is_string()) {
        throw json::ParseError("jsonrpc: missing method");
    }
    r.method = m->as_string();

    const json::Value* p = v.find("params");
    if (p != nullptr && !p->is_null()) {
        r.params_raw = p->dump();
    }

    return r;
}

std::string dump_response(const Response& r) {
    json::Value v = json::Value::object();
    v.set("jsonrpc", json::Value::string("2.0"));
    if (r.has_id) {
        // Emit the id as a number when it looks like one, else as a string.
        // This mirrors what a Go client using json.RawMessage does: the bytes
        // the peer sent come back unchanged. The literal "null" is the one
        // special case, used for parse errors where no id could be read.
        if (r.id_raw == "null") {
            v.set("id", json::Value::null());
        } else if (looks_numeric(r.id_raw)) {
            v.set("id", json::Value::number(r.id_raw));
        } else {
            v.set("id", json::Value::string(r.id_raw));
        }
    } else {
        v.set("id", json::Value::null());
    }

    if (r.is_error) {
        json::Value e = json::Value::object();
        e.set("code", json::Value::number(std::to_string(r.error_code)));
        e.set("message", json::Value::string(r.error_message));
        if (!r.error_data.empty()) {
            e.set("data", json::Value::parse(r.error_data));
        }
        v.set("error", std::move(e));
    } else {
        v.set("result", r.result_raw.empty() ? json::Value::object()
                                             : json::Value::parse(r.result_raw));
    }
    return v.dump();
}

std::string build_notification(const std::string& method, const std::string& params_json) {
    json::Value v = json::Value::object();
    v.set("jsonrpc", json::Value::string("2.0"));
    v.set("method", json::Value::string(method));
    if (!params_json.empty()) {
        v.set("params", json::Value::parse(params_json));
    }
    return v.dump();
}

std::string build_request(const std::string& id_raw, const std::string& method,
                          const std::string& params_json) {
    json::Value v = json::Value::object();
    v.set("jsonrpc", json::Value::string("2.0"));
    v.set("id", json::Value::number(id_raw));
    v.set("method", json::Value::string(method));
    if (!params_json.empty()) {
        v.set("params", json::Value::parse(params_json));
    }
    return v.dump();
}

} // namespace mcp

#include "mcp/server.hpp"

#include "mcp/transport.hpp"

#include <vector>

namespace mcp {
namespace {

// MCP protocol revisions, newest first. Kept in one place so the server and
// the docs cannot drift apart.
const char* kVersions[] = {"2025-06-18", "2025-03-26", "2024-11-05"};

} // namespace

Server::Server(std::string name, std::string version) {
    info_.name = std::move(name);
    info_.version = std::move(version);
}

Server::~Server() = default;

void Server::set_instructions(std::string text) { instructions_ = std::move(text); }

void Server::add_tool(const std::string& name, const std::string& description,
                      const std::string& schema_json, ToolHandler handler) {
    if (name.empty() || !handler) return;
    ToolEntry e;
    e.def.name = name;
    e.def.description = description;
    e.def.input_schema_json = schema_json.empty()
                                  ? R"({"type":"object","properties":{}})"
                                  : schema_json;
    e.handler = std::move(handler);
    std::lock_guard<std::mutex> lock(mu_);
    tools_[name] = std::move(e);
}

void Server::add_resource(const std::string& uri, const std::string& name,
                          const std::string& description, const std::string& mime_type,
                          const std::function<std::string()>& reader) {
    if (uri.empty() || !reader) return;
    ResourceEntry e;
    e.def.uri = uri;
    e.def.name = name;
    e.def.description = description;
    e.def.mime_type = mime_type;
    e.reader = reader;
    std::lock_guard<std::mutex> lock(mu_);
    resources_[uri] = std::move(e);
}

void Server::add_prompt(const std::string& name, const std::string& description,
                        const std::vector<PromptArgument>& arguments,
                        const std::function<std::vector<PromptMessage>(
                            const std::map<std::string, std::string>&)>& render) {
    if (name.empty() || !render) return;
    PromptEntry e;
    e.def.name = name;
    e.def.description = description;
    e.def.arguments = arguments;
    e.render = render;
    std::lock_guard<std::mutex> lock(mu_);
    prompts_[name] = std::move(e);
}

// ---------------------------------------------------------------- schema check

namespace {

// json_type_name returns the JSON Schema type name for a value.
std::string json_type_name(const json::Value& v) {
    switch (v.kind()) {
    case json::Value::Kind::Null:   return "null";
    case json::Value::Kind::Bool:   return "boolean";
    case json::Value::Kind::Number: return "number";
    case json::Value::Kind::String: return "string";
    case json::Value::Kind::Array:  return "array";
    case json::Value::Kind::Object: return "object";
    }
    return "unknown";
}

bool is_whole_number(const json::Value& v) {
    if (!v.is_number()) return false;
    double d = v.as_number();
    return d == static_cast<double>(static_cast<long long>(d));
}

// validate_node is the recursive core, mirroring the Go version.
bool validate_node(const json::Value& schema, const json::Value& value,
                   const std::string& path, std::string& err) {
    const json::Value* type = schema.find("type");
    if (type != nullptr) {
        // Either "string" or a union like ["string","null"]: JSON Schema allows
        // both, and the union is how nullable fields get written. Reading only
        // the string form silently skips validation for those tools, which is
        // worse than rejecting them.
        std::vector<std::string> wants;
        if (type->is_string()) {
            wants.push_back(type->as_string());
        } else if (type->is_array()) {
            for (const auto& t : type->as_array()) {
                if (t.is_string()) wants.push_back(t.as_string());
            }
        }
        if (!wants.empty()) {
            const std::string got = json_type_name(value);
            bool ok = false;
            for (const auto& want : wants) {
                if (want == got) { ok = true; break; }
                // An "integer" field accepts a whole-valued number.
                if (want == "integer" && got == "number" && is_whole_number(value)) {
                    ok = true;
                    break;
                }
            }
            if (!ok) {
                std::string want_list;
                for (size_t i = 0; i < wants.size(); ++i) {
                    if (i) want_list += " | ";
                    want_list += wants[i];
                }
                err = (path.empty() ? "arguments" : path) + ": expected " + want_list +
                      ", got " + got;
                return false;
            }
        }
    }

    const json::Value* en = schema.find("enum");
    if (en != nullptr && en->is_array()) {
        bool found = false;
        for (const auto& allowed : en->as_array()) {
            if (allowed.dump() == value.dump()) {
                found = true;
                break;
            }
        }
        if (!found) {
            err = (path.empty() ? "arguments" : path) + ": value not in enum";
            return false;
        }
    }

    // Array elements. The Go version of this function used to declare items
    // and never read it, which let any element through: the "array" type check
    // above passes, nothing below applies to a list, and the bad value turns
    // up somewhere inside the handler instead of at the boundary.
    const json::Value* items = schema.find("items");
    if (items != nullptr && value.is_array()) {
        size_t index = 0;
        for (const auto& element : value.as_array()) {
            if (!validate_node(*items, element,
                               path + "[" + std::to_string(index) + "]", err)) {
                return false;
            }
            ++index;
        }
    }

    if (!value.is_object()) return true;

    const json::Value* req = schema.find("required");
    if (req != nullptr && req->is_array()) {
        for (const auto& r : req->as_array()) {
            if (!r.is_string()) continue;
            if (!value.has(r.as_string())) {
                err = (path.empty() ? r.as_string() : path + "." + r.as_string()) +
                      ": required but missing";
                return false;
            }
        }
    }

    const json::Value* props = schema.find("properties");
    if (props == nullptr || !props->is_object()) return true;
    for (const auto& kv : props->members()) {
        const json::Value* actual = value.find(kv.first);
        if (actual == nullptr) continue; // absent optional property
        if (!validate_node(kv.second, *actual,
                           path.empty() ? kv.first : path + "." + kv.first, err)) {
            return false;
        }
    }
    return true;
}

} // namespace

bool validate_schema(const json::Value& schema, const json::Value& args, std::string& err) {
    return validate_node(schema, args, "", err);
}

// ------------------------------------------------------------------- dispatch

std::optional<std::string> Server::handle_message(const std::string& raw) {
    Request req;
    try {
        req = parse_request(raw);
    } catch (const json::ParseError& e) {
        // A parse error carries a null id: we never got far enough to read one.
        return dump_response(Response::build_error("null", RpcError(err::kParse, e.what())));
    }
    if (req.method.empty()) {
        return dump_response(Response::build_error(
            req.has_id ? req.id_raw : "null",
            RpcError(err::kInvalidRequest, "method is required")));
    }

    auto out = dispatch(req);
    if (req.is_notification()) {
        // Notifications never get a response, no matter what the handler did.
        return std::nullopt;
    }
    if (!out.has_value()) {
        // The handler declined to answer (only notifications/initialized does
        // this). Answering anyway would desynchronise the client.
        return std::nullopt;
    }
    return out;
}

std::optional<std::string> Server::dispatch(const Request& req) {
    // The handshake is the only method allowed before it completes. Checking
    // once here means a new method cannot forget the guard.
    if (req.method != "initialize") {
        std::lock_guard<std::mutex> lock(mu_);
        if (!initialized_) {
            return dump_response(Response::build_error(
                req.has_id ? req.id_raw : "null",
                RpcError(err::kInvalidRequest, "session not initialized: call initialize first")));
        }
    }

    if (req.method == "initialize") return handle_initialize(req);

    if (req.method == "notifications/initialized") {
        return std::nullopt;
    }
    if (req.method == "ping") {
        json::Value v = json::Value::object();
        return dump_response(Response::build_result(req.id_raw, v.dump()));
    }
    if (req.method == "tools/list") {
        std::lock_guard<std::mutex> lock(mu_);
        json::Value arr = json::Value::array();
        for (const auto& kv : tools_) arr.as_array().push_back(kv.second.def.to_json());
        json::Value res = json::Value::object();
        res.set("tools", std::move(arr));
        return dump_response(Response::build_result(req.id_raw, res.dump()));
    }
    if (req.method == "tools/call") return handle_tools_call(req);

    if (req.method == "resources/list") {
        std::lock_guard<std::mutex> lock(mu_);
        json::Value arr = json::Value::array();
        for (const auto& kv : resources_) arr.as_array().push_back(kv.second.def.to_json());
        json::Value res = json::Value::object();
        res.set("resources", std::move(arr));
        return dump_response(Response::build_result(req.id_raw, res.dump()));
    }
    if (req.method == "resources/templates/list") {
        json::Value res = json::Value::object();
        res.set("resourceTemplates", json::Value::array());
        return dump_response(Response::build_result(req.id_raw, res.dump()));
    }
    if (req.method == "resources/read") return handle_resources_read(req);

    if (req.method == "prompts/list") {
        std::lock_guard<std::mutex> lock(mu_);
        json::Value arr = json::Value::array();
        for (const auto& kv : prompts_) arr.as_array().push_back(kv.second.def.to_json());
        json::Value res = json::Value::object();
        res.set("prompts", std::move(arr));
        return dump_response(Response::build_result(req.id_raw, res.dump()));
    }
    if (req.method == "prompts/get") return handle_prompts_get(req);

    return dump_response(Response::build_error(
        req.has_id ? req.id_raw : "null",
        RpcError(err::kMethodNotFound, "unknown method: " + req.method)));
}

std::string Server::negotiate(const std::string& client_wants) {
    if (client_wants.empty()) return kVersions[0];
    for (const char* v : kVersions) {
        if (client_wants == v) return v;
    }
    // The client asked for something we do not speak. Answer with our newest
    // and let it decide whether to continue.
    return kVersions[0];
}

std::optional<std::string> Server::handle_initialize(const Request& req) {
    std::string client_version;
    std::string client_name;
    if (!req.params_raw.empty()) {
        json::Value p = json::Value::parse(req.params_raw);
        if (const json::Value* v = p.find("protocolVersion")) {
            client_version = v->is_string() ? v->as_string() : "";
        }
        if (const json::Value* ci = p.find("clientInfo")) {
            if (const json::Value* n = ci->find("name")) client_name = n->as_string();
        }
    }
    if (client_name.empty()) {
        return dump_response(Response::build_error(
            req.id_raw, RpcError(err::kInvalidParams, "clientInfo.name is required")));
    }

    {
        std::lock_guard<std::mutex> lock(mu_);
        initialized_ = true;
    }

    json::Value caps = json::Value::object();
    caps.set("tools", json::Value::object());
    caps.set("resources", json::Value::object());
    caps.set("prompts", json::Value::object());

    json::Value res = json::Value::object();
    res.set("protocolVersion", json::Value::string(negotiate(client_version)));
    res.set("capabilities", std::move(caps));
    json::Value si = json::Value::object();
    si.set("name", json::Value::string(info_.name));
    si.set("version", json::Value::string(info_.version));
    res.set("serverInfo", std::move(si));
    if (!instructions_.empty()) {
        res.set("instructions", json::Value::string(instructions_));
    }
    return dump_response(Response::build_result(req.id_raw, res.dump()));
}

std::optional<std::string> Server::handle_tools_call(const Request& req) {
    std::string name;
    json::Value args = json::Value::object();
    if (!req.params_raw.empty()) {
        json::Value p = json::Value::parse(req.params_raw);
        if (const json::Value* n = p.find("name")) name = n->is_string() ? n->as_string() : "";
        if (const json::Value* a = p.find("arguments")) {
            if (a->is_object()) args = *a;
        }
    }
    if (name.empty()) {
        return dump_response(Response::build_error(
            req.id_raw, RpcError(err::kInvalidParams, "tools/call requires a name")));
    }

    ToolEntry entry;
    {
        std::lock_guard<std::mutex> lock(mu_);
        auto it = tools_.find(name);
        if (it == tools_.end()) {
            return dump_response(Response::build_error(
                req.id_raw, RpcError(err::kInvalidParams, "unknown tool: " + name)));
        }
        entry = it->second; // copy so the lock is not held during the call
    }

    json::Value schema = json::Value::parse(entry.def.input_schema_json);
    std::string err;
    if (!validate_schema(schema, args, err)) {
        return dump_response(Response::build_error(
            req.id_raw, RpcError(err::kInvalidParams, "invalid arguments: " + err)));
    }

    std::vector<Content> content;
    try {
        content = entry.handler(args);
    } catch (const ToolError& e) {
        // The tool ran and failed. That is a result with isError, not a
        // protocol error: a client must not retry it.
        CallToolResult r;
        r.is_error = true;
        r.content.push_back(Content::make_text(std::string("tool \"") + name +
                                               "\" failed: " + e.what()));
        return dump_response(Response::build_result(req.id_raw, r.to_json().dump()));
    } catch (const RpcError& e) {
        // A handler may escalate to a protocol error deliberately.
        return dump_response(Response::build_error(req.id_raw, e));
    } catch (const std::exception& e) {
        CallToolResult r;
        r.is_error = true;
        r.content.push_back(Content::make_text(std::string("tool \"") + name +
                                               "\" failed: " + e.what()));
        return dump_response(Response::build_result(req.id_raw, r.to_json().dump()));
    }

    CallToolResult r;
    r.content = std::move(content);
    return dump_response(Response::build_result(req.id_raw, r.to_json().dump()));
}

std::optional<std::string> Server::handle_resources_read(const Request& req) {
    std::string uri;
    if (!req.params_raw.empty()) {
        json::Value p = json::Value::parse(req.params_raw);
        if (const json::Value* u = p.find("uri")) uri = u->is_string() ? u->as_string() : "";
    }
    if (uri.empty()) {
        return dump_response(Response::build_error(
            req.id_raw, RpcError(err::kInvalidParams, "resources/read requires a uri")));
    }

    ResourceEntry entry;
    {
        std::lock_guard<std::mutex> lock(mu_);
        auto it = resources_.find(uri);
        if (it == resources_.end()) {
            // -32002, not -32602: the request was well formed, the resource
            // just does not exist.
            return dump_response(Response::build_error(
                req.id_raw, RpcError(err::kResourceNotFound, "no resource at " + uri)));
        }
        entry = it->second;
    }

    ResourceContents c;
    c.uri = uri;
    c.mime_type = entry.def.mime_type;
    c.text = entry.reader();

    json::Value res = json::Value::object();
    json::Value arr = json::Value::array();
    arr.as_array().push_back(c.to_json());
    res.set("contents", std::move(arr));
    return dump_response(Response::build_result(req.id_raw, res.dump()));
}

std::optional<std::string> Server::handle_prompts_get(const Request& req) {
    std::string name;
    std::map<std::string, std::string> args;
    if (!req.params_raw.empty()) {
        json::Value p = json::Value::parse(req.params_raw);
        if (const json::Value* n = p.find("name")) name = n->is_string() ? n->as_string() : "";
        if (const json::Value* a = p.find("arguments")) {
            if (a->is_object()) {
                for (const auto& kv : a->members()) {
                    if (kv.second.is_string()) args[kv.first] = kv.second.as_string();
                }
            }
        }
    }
    if (name.empty()) {
        return dump_response(Response::build_error(
            req.id_raw, RpcError(err::kInvalidParams, "prompts/get requires a name")));
    }

    PromptEntry entry;
    {
        std::lock_guard<std::mutex> lock(mu_);
        auto it = prompts_.find(name);
        if (it == prompts_.end()) {
            return dump_response(Response::build_error(
                req.id_raw, RpcError(err::kInvalidParams, "unknown prompt: " + name)));
        }
        entry = it->second;
    }

    for (const auto& a : entry.def.arguments) {
        if (a.required) {
            auto found = args.find(a.name);
            if (found == args.end() || found->second.empty()) {
                return dump_response(Response::build_error(
                    req.id_raw,
                    RpcError(err::kInvalidParams,
                             "prompt \"" + name + "\" requires argument \"" + a.name + "\"")));
            }
        }
    }

    json::Value res = json::Value::object();
    if (!entry.def.description.empty()) {
        res.set("description", json::Value::string(entry.def.description));
    }
    json::Value arr = json::Value::array();
    for (const auto& m : entry.render(args)) arr.as_array().push_back(m.to_json());
    res.set("messages", std::move(arr));
    return dump_response(Response::build_result(req.id_raw, res.dump()));
}

void Server::serve(Transport& t) {
    while (auto msg = t.read_message()) {
        std::optional<std::string> out;
        try {
            out = handle_message(*msg);
        } catch (const std::exception& e) {
            // A single bad message must not end the server, and above all must
            // not end the process. Everything reachable from dispatch --
            // re-parsing a tool's declared schema, a tool handler throwing
            // something other than RpcError -- lands here, and without this
            // guard it propagates out of main. The id is unknown at this
            // point, so the reply carries null, which is what JSON-RPC
            // prescribes for a failure we cannot attribute.
            StdioTransport::log_to_stderr(std::string("serve: ") + e.what());
            out = dump_response(Response::build_error(
                "null", RpcError(err::kInternal, std::string("internal error: ") + e.what())));
        } catch (...) {
            // Not derived from std::exception: there is nothing useful to say
            // about it, and catching only std::exception would let it through
            // to terminate.
            StdioTransport::log_to_stderr("serve: unknown non-standard exception");
            out = dump_response(Response::build_error(
                "null", RpcError(err::kInternal, "internal error")));
        }
        if (out.has_value()) t.write_message(*out);
    }
}

} // namespace mcp

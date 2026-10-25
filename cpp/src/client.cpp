#include "mcp/client.hpp"

#include "mcp/json.hpp"

namespace mcp {

Client::Client(Transport& t, std::string name, std::string version)
    : t_(t) {
    src_.name = std::move(name);
    src_.version = std::move(version);
}

Client::~Client() {
    {
        std::lock_guard<std::mutex> lock(mu_);
        stopped_ = true;
    }
    cv_.notify_all();
    if (reader_.joinable()) reader_.join();
}

void Client::connect() {
    // The reader thread starts before the first request goes out. With a
    // synchronous pipe the server's response blocks until somebody reads it,
    // so a client that starts reading afterwards deadlocks against its own
    // server.
    reader_ = std::thread(&Client::read_loop, this);

    json::Value params = json::Value::object();
    params.set("protocolVersion", json::Value::string("2025-06-18"));
    params.set("capabilities", json::Value::object());
    json::Value ci = json::Value::object();
    ci.set("name", json::Value::string(src_.name));
    ci.set("version", json::Value::string(src_.version));
    params.set("clientInfo", std::move(ci));

    std::string raw = call("initialize", params.dump());

    json::Value res = json::Value::parse(raw);
    if (const json::Value* v = res.find("protocolVersion")) protocol_version_ = v->as_string();
    if (const json::Value* v = res.find("instructions")) instructions_ = v->as_string();
    if (const json::Value* si = res.find("serverInfo")) {
        if (const json::Value* n = si->find("name")) server_info_.name = n->as_string();
        if (const json::Value* v = si->find("version")) server_info_.version = v->as_string();
    }

    t_.write_message(build_notification("notifications/initialized", ""));
}

std::string Client::call(const std::string& method, const std::string& params_json) {
    std::string id;
    {
        std::lock_guard<std::mutex> lock(mu_);
        id = std::to_string(++next_id_);
    }

    t_.write_message(build_request(id, method, params_json));

    // Wait for the reader thread to file the result under this id. The
    // condition variable is woken for every message, so a notification
    // arriving first costs a spurious wakeup and nothing more.
    std::unique_lock<std::mutex> lock(mu_);
    cv_.wait(lock, [&] {
        return stopped_ || results_.count(id) > 0 || errors_.count(id) > 0;
    });

    auto eit = errors_.find(id);
    if (eit != errors_.end()) {
        RpcError e = eit->second;
        errors_.erase(eit);
        lock.unlock();
        throw e;
    }
    auto rit = results_.find(id);
    if (rit == results_.end()) {
        throw RpcError(err::kInternal, "connection closed while waiting for " + method);
    }
    std::string out = rit->second;
    results_.erase(rit);
    return out;
}

void Client::read_loop() {
    while (auto msg = t_.read_message()) {
        // An empty message is a real answer to a notification: HTTP answers
        // one with 202 and no body. It carries nothing to parse, and treating
        // it as end-of-stream would retire this thread mid-conversation.
        if (msg->empty()) continue;

        // Everything below runs inside a try because this is a thread's entry
        // point. An exception that escapes here becomes std::terminate, taking
        // the whole process with it -- so one malformed line, or one server
        // that printed a log message to stdout instead of stderr, would kill
        // the client rather than the connection. Dropping what we cannot
        // parse is the same policy the Go readLoop follows.
        try {
            // Parse as a generic object, not as a request. A response has no
            // "method" field, so running it through parse_request throws and
            // the result would be dropped on the floor -- which looks exactly
            // like a hung client. Only id, result and error are looked at here.
            json::Value v = json::Value::parse(*msg);

            const json::Value* idv = v.find("id");
            if (idv == nullptr) continue; // notification: nobody asked for it

            std::string id;
            if (idv->is_number()) {
                id = idv->as_number_literal();
            } else if (idv->is_string()) {
                id = idv->as_string();
            } else {
                continue; // id is an object or array: not ours to answer
            }

            std::lock_guard<std::mutex> lock(mu_);
            const json::Value* errv = v.find("error");
            if (errv != nullptr) {
                RpcError e(0, "");
                if (const json::Value* c = errv->find("code")) {
                    if (c->is_number()) e.code = static_cast<int>(c->as_number());
                }
                if (const json::Value* m = errv->find("message")) {
                    if (m->is_string()) e.message = m->as_string();
                }
                errors_[id] = e;
            } else if (const json::Value* r = v.find("result")) {
                results_[id] = r->dump();
            }
            cv_.notify_all();
        } catch (const std::exception&) {
            continue;
        }
    }

    std::lock_guard<std::mutex> lock(mu_);
    stopped_ = true;
    cv_.notify_all();
}

std::vector<Tool> Client::list_tools() {
    std::string raw = call("tools/list", "");
    json::Value v = json::Value::parse(raw);
    std::vector<Tool> out;
    if (const json::Value* arr = v.find("tools")) {
        for (const auto& t : arr->as_array()) {
            Tool tool;
            if (const json::Value* n = t.find("name")) tool.name = n->as_string();
            if (const json::Value* d = t.find("description")) tool.description = d->as_string();
            if (const json::Value* s = t.find("inputSchema")) tool.input_schema_json = s->dump();
            out.push_back(tool);
        }
    }
    return out;
}

CallToolResult Client::call_tool(const std::string& name, const json::Value& args) {
    json::Value params = json::Value::object();
    params.set("name", json::Value::string(name));
    params.set("arguments", args);
    std::string raw = call("tools/call", params.dump());

    CallToolResult res;
    json::Value v = json::Value::parse(raw);
    if (const json::Value* ie = v.find("isError")) res.is_error = ie->as_bool();
    if (const json::Value* arr = v.find("content")) {
        for (const auto& c : arr->as_array()) res.content.push_back(Content::from_json(c));
    }
    return res;
}

std::vector<Resource> Client::list_resources() {
    std::string raw = call("resources/list", "");
    json::Value v = json::Value::parse(raw);
    std::vector<Resource> out;
    if (const json::Value* arr = v.find("resources")) {
        for (const auto& r : arr->as_array()) {
            Resource res;
            if (const json::Value* u = r.find("uri")) res.uri = u->as_string();
            if (const json::Value* n = r.find("name")) res.name = n->as_string();
            if (const json::Value* d = r.find("description")) res.description = d->as_string();
            if (const json::Value* m = r.find("mimeType")) res.mime_type = m->as_string();
            out.push_back(res);
        }
    }
    return out;
}

std::vector<ResourceContents> Client::read_resource(const std::string& uri) {
    json::Value params = json::Value::object();
    params.set("uri", json::Value::string(uri));
    std::string raw = call("resources/read", params.dump());

    std::vector<ResourceContents> out;
    json::Value v = json::Value::parse(raw);
    if (const json::Value* arr = v.find("contents")) {
        for (const auto& c : arr->as_array()) {
            ResourceContents rc;
            if (const json::Value* u = c.find("uri")) rc.uri = u->as_string();
            if (const json::Value* m = c.find("mimeType")) rc.mime_type = m->as_string();
            if (const json::Value* t = c.find("text")) rc.text = t->as_string();
            out.push_back(rc);
        }
    }
    return out;
}

std::vector<Prompt> Client::list_prompts() {
    std::string raw = call("prompts/list", "");
    json::Value v = json::Value::parse(raw);
    std::vector<Prompt> out;
    if (const json::Value* arr = v.find("prompts")) {
        for (const auto& p : arr->as_array()) {
            Prompt prompt;
            if (const json::Value* n = p.find("name")) prompt.name = n->as_string();
            if (const json::Value* d = p.find("description")) prompt.description = d->as_string();
            out.push_back(prompt);
        }
    }
    return out;
}

std::vector<PromptMessage> Client::get_prompt(const std::string& name,
                                              const std::map<std::string, std::string>& args) {
    json::Value params = json::Value::object();
    params.set("name", json::Value::string(name));
    if (!args.empty()) {
        json::Value a = json::Value::object();
        for (const auto& kv : args) a.set(kv.first, json::Value::string(kv.second));
        params.set("arguments", std::move(a));
    }
    std::string raw = call("prompts/get", params.dump());

    std::vector<PromptMessage> out;
    json::Value v = json::Value::parse(raw);
    if (const json::Value* arr = v.find("messages")) {
        for (const auto& m : arr->as_array()) {
            PromptMessage pm;
            if (const json::Value* r = m.find("role")) pm.role = r->as_string();
            if (const json::Value* c = m.find("content")) pm.content = Content::from_json(*c);
            out.push_back(pm);
        }
    }
    return out;
}

void Client::ping() { call("ping", ""); }

} // namespace mcp

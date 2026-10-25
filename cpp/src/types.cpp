#include "mcp/types.hpp"

namespace mcp {

json::Value Content::to_json() const {
    json::Value v = json::Value::object();
    switch (kind) {
    case Kind::Text:
        v.set("type", json::Value::string("text"));
        v.set("text", json::Value::string(text));
        break;
    case Kind::Image:
        v.set("type", json::Value::string("image"));
        v.set("data", json::Value::string(data));
        v.set("mimeType", json::Value::string(mime_type));
        break;
    case Kind::Audio:
        v.set("type", json::Value::string("audio"));
        v.set("data", json::Value::string(data));
        v.set("mimeType", json::Value::string(mime_type));
        break;
    case Kind::Resource:
        v.set("type", json::Value::string("resource"));
        json::Value r = json::Value::object();
        r.set("uri", json::Value::string(resource_uri));
        if (!mime_type.empty()) r.set("mimeType", json::Value::string(mime_type));
        r.set("text", json::Value::string(resource_text));
        v.set("resource", std::move(r));
        break;
    }
    return v;
}

Content Content::from_json(const json::Value& v) {
    Content c;
    const json::Value* type = v.find("type");
    if (type == nullptr || !type->is_string()) return c;
    const std::string& t = type->as_string();
    if (t == "text") {
        c.kind = Kind::Text;
        if (const json::Value* x = v.find("text")) c.text = x->is_string() ? x->as_string() : "";
    } else if (t == "image" || t == "audio") {
        c.kind = (t == "image") ? Kind::Image : Kind::Audio;
        if (const json::Value* x = v.find("data")) c.data = x->is_string() ? x->as_string() : "";
        if (const json::Value* x = v.find("mimeType")) c.mime_type = x->is_string() ? x->as_string() : "";
    } else if (t == "resource") {
        c.kind = Kind::Resource;
        if (const json::Value* r = v.find("resource")) {
            if (const json::Value* x = r->find("uri")) c.resource_uri = x->as_string();
            if (const json::Value* x = r->find("mimeType")) c.mime_type = x->as_string();
            if (const json::Value* x = r->find("text")) c.resource_text = x->as_string();
        }
    }
    return c;
}

json::Value Tool::to_json() const {
    json::Value v = json::Value::object();
    v.set("name", json::Value::string(name));
    if (!description.empty()) v.set("description", json::Value::string(description));
    v.set("inputSchema", input_schema_json.empty()
                             ? json::Value::parse(R"({"type":"object","properties":{}})")
                             : json::Value::parse(input_schema_json));
    return v;
}

json::Value CallToolResult::to_json() const {
    json::Value v = json::Value::object();
    json::Value arr = json::Value::array();
    for (const auto& c : content) arr.as_array().push_back(c.to_json());
    v.set("content", std::move(arr));
    if (is_error) v.set("isError", json::Value::boolean(true));
    return v;
}

json::Value Resource::to_json() const {
    json::Value v = json::Value::object();
    v.set("uri", json::Value::string(uri));
    v.set("name", json::Value::string(name));
    if (!description.empty()) v.set("description", json::Value::string(description));
    if (!mime_type.empty()) v.set("mimeType", json::Value::string(mime_type));
    return v;
}

json::Value ResourceContents::to_json() const {
    json::Value v = json::Value::object();
    v.set("uri", json::Value::string(uri));
    if (!mime_type.empty()) v.set("mimeType", json::Value::string(mime_type));
    v.set("text", json::Value::string(text));
    return v;
}

json::Value PromptArgument::to_json() const {
    json::Value v = json::Value::object();
    v.set("name", json::Value::string(name));
    if (!description.empty()) v.set("description", json::Value::string(description));
    if (required) v.set("required", json::Value::boolean(true));
    return v;
}

json::Value Prompt::to_json() const {
    json::Value v = json::Value::object();
    v.set("name", json::Value::string(name));
    if (!description.empty()) v.set("description", json::Value::string(description));
    if (!arguments.empty()) {
        json::Value arr = json::Value::array();
        for (const auto& a : arguments) arr.as_array().push_back(a.to_json());
        v.set("arguments", std::move(arr));
    }
    return v;
}

json::Value PromptMessage::to_json() const {
    json::Value v = json::Value::object();
    v.set("role", json::Value::string(role));
    v.set("content", content.to_json());
    return v;
}

} // namespace mcp

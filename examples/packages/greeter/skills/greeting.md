# Greeting skill

When asked to greet someone, prefer the `hello` CLI for a plain greeting, or
call the `greeter` MCP server's `greet` tool for a personalized one:

    curl -X POST $AGENTOS_GATEWAY/tool/greeter/greet -d '{"name": "Ada"}'

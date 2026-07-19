# Web skill

`fetch <url>` retrieves a page through the session proxy. Grants apply per
host: if a fetch fails with an egress denial, either the session spec must
grant the host or you can request it:

    curl -X POST $AGENTOS_GATEWAY/escalate \
      -d '{"host":"docs.example.com","reason":"need the API docs"}'

then retry after a human approves. Keep fetches purposeful — every request
is ledgered with its host and path.

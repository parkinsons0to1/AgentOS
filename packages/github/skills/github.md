# GitHub skill

Use `gh-api` for all GitHub operations. It routes through the gateway with
authentication injected by the broker — never attach your own tokens.

Common calls:

    gh-api /user                                  # who am I
    gh-api /user/repos?per_page=10                # my repositories
    gh-api /repos/OWNER/REPO/issues               # list issues
    gh-api -X POST /repos/OWNER/REPO/issues '{"title":"...","body":"..."}'
    gh-api /repos/OWNER/REPO/pulls                # list PRs
    gh-api /search/code?q=QUERY+repo:OWNER/REPO   # search code

Rate limits and errors come back as GitHub's JSON; a 403 from the *gateway*
(body contains `"egress"`) means this session lacks the grant — request it via
`POST $AGENTOS_GATEWAY/escalate` and a human will decide.

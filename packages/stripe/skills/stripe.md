# Stripe skill

Use `stripe-api` for all Stripe operations. Authentication is injected by the
broker; bodies are form-encoded per Stripe convention.

    stripe-api /v1/balance
    stripe-api /v1/customers?limit=10
    stripe-api -X POST /v1/customers "email=ada@example.com&name=Ada"
    stripe-api /v1/invoices?customer=cus_123

Money moves through this API. Prefer read calls; for anything that creates
charges, refunds, or payouts, state what you're about to do and why in your
output before doing it, so the ledger tells a reviewable story.

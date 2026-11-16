# Shared placement contract

Source copy of the MIT-licensed go-carbon placement v1 contract from
[go-carbon PR 1476](https://github.com/go-graphite/go-carbon/pull/1476), as integrated
in commit `73738a651e94d7cd7e6b8cb26382c03f85d93ce1`. The upstream durable authority is not copied: this relay
has no durable authority or replay adapter yet. See [relay integration](../../docs/placement-routing.md).

`routing.go`, `map.go`, `json.go`, `receipts.go` and both JSON fixtures preserve
the upstream contract. `Document.Digest()` hashes typed, compact Go JSON with
field/array order and nil-array semantics intact. Unknown and duplicate fields
are rejected. Control code must use `ValidateTransition`, not only topology
validation, for epoch adoption. Receipts are trusted adapter attestations, not
cryptographic proof of stored history or authenticated transport identities.

Canonical map fixture digest:
`943319b0cc9be1b2061acf9db29fa03bbbe56d24e51112207b06e723d4988354`.
The fixture's raw-file SHA-256 differs because its JSON is indented.

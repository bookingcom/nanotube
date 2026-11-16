# Shared placement contract

MIT-licensed source copy of the go-carbon placement v1 contract from
[PR 1476](https://github.com/go-graphite/go-carbon/pull/1476), integrated in
`73738a651e94d7cd7e6b8cb26382c03f85d93ce1`.

Routing and both golden fixtures preserve the upstream contract. The optional
replica `moving_to` field is a relay extension for dual-owner routing. Absent
fields preserve canonical map bytes/digests; older strict decoders reject maps
that use the extension. Store/controller support must land before moving maps
are enabled. See [relay integration](../../docs/placement-routing.md).

`Document.Digest()` hashes typed compact Go JSON in field/array order, preserving
nil-array semantics. Unknown and duplicate fields are rejected. The historical
receipt types remain as copied contract helpers, but the relay does not use
ACK/fence/frontier activation. Its adoption validates successors and explicit
donor/destination promotion without a durable output subsystem.

Canonical map fixture digest:
`943319b0cc9be1b2061acf9db29fa03bbbe56d24e51112207b06e723d4988354`.
The raw-file SHA-256 differs because the JSON is indented.

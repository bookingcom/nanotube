# Configuration examples and explanation

Here are the examples of configuration together with explanation how it works.

## Routing

Is defined in the [rules config](docs/config/rules.toml) that is in turn referred to in the [main config](docs/config/config.toml). This is how it works:

- Routing rules are applied to each incoming record in order;
  - If regex or prefix in a rule matches, the record is sent to the clusters in the `clusters` list;
    - If `continue` is set to `true`, continue matching next rules, stop otherwise. `false` is the default;
  - If no regex or prefix matches, continue down the list of rules;
- Multiple rules can be matched to each record;
- Each record is sent to a specific cluster only once. If two rules send it to same cluster, only one instance will be sent;
- Cluster names must be from the set in the [clusters config](docs/config/clusters.toml).

### Rewrites

Optionally, it is possible to apply the [rewrite rules](docs/config/rewrite.toml). This is how they work:

- rewrites are applied before the routing;
- all rules are applied to each record one-by-one in order. The record may be modified along the way;
- rule matches if `from` matches;
  - then metric path is rewritten to `to` in place;
  - if `copy` is `true` the original metric is copied and sent directly to be routed skipping the following re-writes. `copy` is `false` be default.

## Record validation and normalization

Nanotube checks every record it receives, if it consists of three fields. If not, it logs a warning.

It is possible to turn on record normalization with the `NormalizeRecords` option in general configuration. It is on by default. Normalizations performed are mainly for backward compatibility with `carbon-c-relay`, namely:

- Double or more subsequent dots in path are converted to a single dot: *a.b..c* -> *a.b.c*
- Prefix and trailing dots in path are removed: *.a.b.c.* -> *a.b.c*
- Spaces in record are normalized to a single space character: *a.b.c␣␣␣1.23\t1234567* -> *a.b.c␣1.23␣1234567*
- Characters not in the set `[0-9a-zA-Z-_:#]` are replaced with `_`. Tags have their own allowed chars but are not supported for now.

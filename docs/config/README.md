# Configuration examples and explanation

Here are the examples of configuration together with explanation how it works.

## Routing

Is defined in the [rules config](rules.toml) that is in turn referred to in the [main config](config.toml). This is how it works:

- Routing rules are applied to each incoming record in order;
  - If regex or prefix in a rule matches, the record is sent to the clusters in the `clusters` list;
    - If `continue` is set to `true`, continue matching next rules, stop otherwise. `false` is the default;
  - If no regex or prefix matches, continue down the list of rules;
- Multiple rules can be matched to each record;
- Each record is sent to a specific cluster only once. If two rules send it to same cluster, only one instance will be sent;
- Cluster names must be from the set in the [clusters config](clusters.toml).

### Rewrites

Optionally, it is possible to apply the [rewrite rules](rewrite.toml). This is how they work:

- rewrites are applied before the routing;
- all rules are applied to each record one-by-one in order. The record may be modified along the way;
- rule matches if `from` matches;
  - then metric path is rewritten to `to` in place;
  - if `copy` is `true` the original metric is copied and sent directly to be routed skipping the following re-writes. `copy` is `false` by default.

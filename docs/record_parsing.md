# Record parsing and requirements

## Record validation and normalization

Nanotube checks every record it receives, if it consists of three fields. If not, it logs a warning.

It is possible to turn on record normalization with the `NormalizeRecords` option in general configuration. It is on by default. Normalizations performed are mainly for backward compatibility with `carbon-c-relay`, namely:

- Double or more subsequent dots in path are converted to a single dot: `a.b..c` -> `a.b.c`
- Prefix and trailing dots in path are removed: `.a.b.c.` -> `a.b.c`
- Spaces in record are normalized to a single space character: `a.b.c␣␣␣1.23\t1234567` -> `a.b.c␣1.23␣1234567`
- Characters not in the set `[0-9a-zA-Z-_:#]` are replaced with `_`.

## Parsing algorithm

Here's how we parse the records in Graphite line protocol.

1. Record is broken into 3 tokens separated by any combination of spaces `' '` or `'\t'`.
2. If normalization is enabled, the first token - which is the path - gets normalized:
   1. Leading and trailing dots `.` are removed.
   2. Duplicated dots are merged, e.g. `a...b` becomes `a.b`.
   3. If character is not one of `a-zA-Z0-9` or `:_-#.`, it is replaced by `_`.

## Requirements for the records

* All records have to be in ASCII.
* On top of that, only characters `a-zA-Z0-9` or `:_-#.` are permitted.
* When sending to TCP or UDP, records have to be separated by `\n`.
* When sending via UDP, records cannot be split between datagrams.

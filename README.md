![CI](https://github.com/bookingcom/nanotube/workflows/CI/badge.svg)


Nanotube: data router for Graphite
=================================

This is the router (or relay, or reverse-proxy) for Graphite. It routes incoming records according to the specified rules. Nanotube is designed for high-load systems. It is used at Booking.com to route up to `400k` incoming and `1.2M` outgoing records/sec on a single box.

Record structure
----------------

The only supported protocol is [line](https://graphite.readthedocs.io/en/latest/feeding-carbon.html#the-plaintext-protocol). The records should follow the structure:
```
path.path.path value datetime
```

Config
------

Please refer to the sample configs in the _config_ folder for examples and documenation.

Design
------

Please refer to the design doc _docs/design.md_.


Record validation and normalization
-----------------------------------

It is possible to turn on record normalization with the `NormalizeRecords` option in general configuration. It is on by default.

Validation checks performed are:

- does the record consist of three fields
- is value in a record a valid real number
- is time in the record a valid epoch

Normalizations performed are mainly for backward compatibility with `carbon-c-relay`, namely:

- Double or more subsequent dots in path are converted to a single dot. E.g. _a.b..c_ -> _a.b.c_
- Prefix and trailing dots in path are removed. E.g. _.a.b.c._ -> _a.b.c_
- Spaces in record are normalized to a single space character. E.g. _a.b.c␣␣␣1.23<tab>1234567_ -> _a.b.c␣1.23␣1234567_
- Chars not in `[0-9a-zA-Z-_:#]` are replaced with `_`. Tags have their own allowed chars and are not supported for now.

Acknowledgement
---------------------------

This program was originally developed for Booking.com. With approval from Booking.com, the code was generalised and published as Open Source on GitHub, for which the authors would like to express their gratitude.

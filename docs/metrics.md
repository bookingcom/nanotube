# Nanotube metrics

Metrics are exposed on the `/` endpoint on the port defined in the config as `PromPort`. Default port is *9090*.

## The list of metrics

1. `in_records_total` counts the number of all records (or data points) that arrive to Nanotube to be routed. Does not include throttled records.
2. `out_records_total` counts out records by destination cluster (the `cluster` tag) and host (the `host` tag).
3. `throttled_records_total` counts dropped records because the main queue is full.
4. `throttled_host_records_total` counts dropped records from host queue because it's full.
5. `blackholed_records_total` counts records sent to the 'blackhole' cluster.
6. `main_queue_length` the current length of the main queue. See design doc for details. Updated every second.
7. `host_queue_length_bucket` distribution of host queue sizes as histogram. Updated with lower frequency than incoming records. Histogram is chosen over summary for performance and aggregation possibility. The upper bound of the histogram is defined by the `HostQueueSize` config parameter that defines max number of elements in the queue.
8. `processing_duration_seconds_bucket` Histogram with time to process each record.
9. `active_connections` number of current connections to nanotube. Updated every time someone connects and disconnects to the Nanotube.
10. `open_in_connections_total` number of incoming connections to nanotube. Updated every time someone connects to Nanotube.

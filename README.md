# s4
S3 Stress (S4) and Benchmark Swiss Army Knife

![](https://github.com/gbunt/s4/workflows/Build/badge.svg)

## Usage

- Clone this repository or just download `bin/linux/s4` and configuration files from `configs`
- Create a Bucket on S3
- Store S3 credentials in `~/.aws/credentials`. For more info see: https://docs.aws.amazon.com/cli/latest/userguide/cli-config-files.html
- Edit or create a yaml configuration file (see `configs` for examples)
- Before you can test GET ops, first PUT enough data to S3 by running `s4 -c <yaml configuration for writes>`. Example 100% PUT test:

```
ubuntu@golang-dev:~$ ./s4 -c configs/writes.yaml
--- config:
{http://s3-region.example.com false s4_test 0 false [{job1 1Mib 75 100} {job2 4Mib 75 100}] []}

2018/05/21 13:04:15 queued/read: 0/   0, queued/write:  15000/ 234, byte/s: 657 MiB
2018/05/21 13:04:16 queued/read: 0/   0, queued/write:  14766/ 335, byte/s: 965 MiB
2018/05/21 13:04:17 queued/read: 0/   0, queued/write:  14431/ 361, byte/s: 1015 MiB
2018/05/21 13:04:18 queued/read: 0/   0, queued/write:  14070/ 351, byte/s: 996 MiB
2018/05/21 13:04:19 queued/read: 0/   0, queued/write:  13719/ 377, byte/s: 1.0 GiB
2018/05/21 13:04:20 queued/read: 0/   0, queued/write:  13342/ 299, byte/s: 818 MiB
2018/05/21 13:04:21 queued/read: 0/   0, queued/write:  13043/ 369, byte/s: 1.0 GiB
2018/05/21 13:04:22 queued/read: 0/   0, queued/write:  12673/ 357, byte/s: 987 MiB
2018/05/21 13:04:23 queued/read: 0/   0, queued/write:  12316/ 318, byte/s: 885 MiB
2018/05/21 13:04:24 queued/read: 0/   0, queued/write:  11998/ 322, byte/s: 883 MiB
2018/05/21 13:04:25 queued/read: 0/   0, queued/write:  11676/ 320, byte/s: 881 MiB
2018/05/21 13:04:26 queued/read: 0/   0, queued/write:  11356/ 322, byte/s: 901 MiB
2018/05/21 13:04:27 queued/read: 0/   0, queued/write:  11034/ 370, byte/s: 1.0 GiB
2018/05/21 13:04:28 queued/read: 0/   0, queued/write:  10664/ 322, byte/s: 919 MiB
2018/05/21 13:04:29 queued/read: 0/   0, queued/write:  10342/ 304, byte/s: 829 MiB
2018/05/21 13:04:30 queued/read: 0/   0, queued/write:  10038/ 293, byte/s: 800 MiB
2018/05/21 13:04:31 queued/read: 0/   0, queued/write:   9745/ 335, byte/s: 911 MiB
2018/05/21 13:04:32 queued/read: 0/   0, queued/write:   9410/ 314, byte/s: 869 MiB
2018/05/21 13:04:33 queued/read: 0/   0, queued/write:   9096/ 350, byte/s: 992 MiB
```

- After all threads * iterations have finished or ctrl-c is pressed, the total result is reported:

```
2018/05/21 13:04:34 Elapsed time in seconds: 20.920798
2018/05/21 13:04:34 Total OPS: 6536, operations per second: 326, bytes per second: 912 MiB
```

- Now do a read benchmark, or simulate a mixed workload

> Optionally use `-l` during a write or mixed workload to log all Object names and md5 hashes.

## Configuration

Configuration is very simple. Example:

```yaml
# s4 configuration
s3_endpoint: http://s3-region.example.com
bucket: s4_test
read_range_max: 100
# read_sparse: false
# random_data: false
# tls_no_verify: false
# disable_keepalive: false

read:
  - name: job1
    recordsize: 1mib
    threadcount: 10
    iterations: 100
  - name: job2
    recordsize: 4mib
    threadcount: 10
    iterations: 100

write:
  - name: job1
    recordsize: 1mib
    threadcount: 10
    iterations: 100
  - name: job2
    recordsize: 4mib
    threadcount: 10
    iterations: 100
```

The above would be a mixed read/write test with 2 jobs for both reads as writes with 1mib and 4mib Object sizes each

#### Options
##### s3_endpoint
The S3 endpoint to test against

##### bucket
The name of the Bucket in S3. Create the Bucket before testing

##### read_range_max
When testing reads, test against range `read_range_max` Objects. Eg. when set to 100 `s4` will (re-)read from the 1st 100 Objects

##### read_sparse
With reads, when set to `true` `s4` reads response ContentLength and does not wait to stream the full payload. Defaults to `false`

##### random_data
When set to `true` S4 generates psuedo-random data

##### tls_no_verify
When using HTTPS and set to `true`, this disables certificate verification. Defaults to `false`

#### disable_keepalive
Disable keepalive connections. When doing DNS-based balancing across multiple nodes with keepalive enabled (default), most threads will use the same IP address. Disabling keepalive will result in a more balanced distribution of requests. Note: Does impact throughput.

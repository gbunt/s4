package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithyendpoints "github.com/aws/smithy-go/endpoints"
	"github.com/dustin/go-humanize"
	"gopkg.in/yaml.v2"
)

type Stats struct {
	reads, writes, errors, bytes int64
	total_writes, total_reads    int64
}

type Job struct {
	Name        string `yaml:"name"`
	Recordsize  string `yaml:"recordsize"`
	Threadcount int    `yaml:"threadcount"`
	Iterations  int    `yaml:"iterations"`
}

type Configs struct {
	S3Endpoint   string `yaml:"s3_endpoint"`
	NoVerifyTLS  bool   `yaml:"tls_no_verify"`
	NoKeepalive  bool   `yaml:"disable_keepalive"`
	AbortOnError bool   `yaml:"abort_on_error"`
	RandomData   bool   `yaml:"random_data"`
	Bucket       string `yaml:"bucket"`
	ReadRange    int    `yaml:"read_range_max"`
	ReadSparse   bool   `yaml:"read_sparse"`
	Write        []Job  `yaml:"write"`
	Read         []Job  `yaml:"read"`
}

var stats Stats
var writeGroup sync.WaitGroup
var readGroup sync.WaitGroup
var config Configs
var m sync.Mutex

var c = sync.NewCond(&m)
var startRun bool
var then = time.Now()

const logFile = "s4.log"

// AWS resolverV2 implementation
type resolverV2 struct {
	endpoint string
}

func (r *resolverV2) ResolveEndpoint(_ context.Context, params s3.EndpointParameters) (
	smithyendpoints.Endpoint, error) {
	path := r.endpoint
	if params.Bucket != nil {
		path += "/" + *params.Bucket
	}
	u, err := url.Parse(path)
	if err != nil {
		return smithyendpoints.Endpoint{}, err
	}
	return smithyendpoints.Endpoint{URI: *u}, nil
}

func getService() *s3.Client {
	httpClient := http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: config.NoVerifyTLS},
			Proxy:           http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			DisableKeepAlives:     config.NoKeepalive,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConnsPerHost:   100,
			TLSHandshakeTimeout:   3 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}

	cfg, err := awscfg.LoadDefaultConfig(context.TODO(), awscfg.WithRegion("region1"), awscfg.WithHTTPClient(&httpClient))
	if err != nil {
		panic(err)
	}

	svc := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.EndpointResolverV2 = &resolverV2{fmt.Sprintf("http://%s.%s", "region1", config.S3Endpoint)}
		o.EndpointOptions.DisableHTTPS = true
	})

	return svc
}

func s3_downloader(start int, stop int, recordSize string) int {
	defer readGroup.Done()

	atomic.AddInt64(&stats.total_reads, int64(stop-start))
	c.L.Lock()
	for !startRun {
		c.Wait()
	}
	c.L.Unlock()

	svc := getService()

	d, ferr := os.OpenFile("/dev/null", os.O_APPEND|os.O_WRONLY, os.ModeAppend)
	if ferr != nil {
		log.Fatal("Cannot open output file")
		panic(ferr)
	}

	// collect object names
	params := &s3.ListObjectsInput{
		Bucket:    aws.String(config.Bucket),
		Delimiter: aws.String("/"),
		MaxKeys:   aws.Int32(5000),
		Prefix:    aws.String(recordSize + "/"),
	}
	resp, err := svc.ListObjects(context.TODO(), params)
	if err != nil {
		panic(err.Error())
	}
	objects := make([]string, 1, config.ReadRange)
	for _, item := range resp.Contents {
		objects = append(objects, *item.Key)
	}

	for i := 0; i < stop; i++ {
		n := 1 + rand.Intn(config.ReadRange)
		k := aws.String(objects[n])
		params := &s3.GetObjectInput{
			Bucket: aws.String(config.Bucket), // Required
			Key:    k,
		}
		resp, err := svc.GetObject(context.TODO(), params)
		if err != nil {
			if config.AbortOnError {
				panic(err)
			}

			atomic.AddInt64(&stats.errors, 1)
			fmt.Println(err.Error())
		} else {
			if !config.ReadSparse {
				// stream data to fh
				if _, err := io.Copy(d, resp.Body); err != nil {
					panic(err)
				}
			}
			atomic.AddInt64(&stats.bytes, *resp.ContentLength)
			atomic.AddInt64(&stats.reads, 1)
			if err := resp.Body.Close(); err != nil {
				panic(err)
			}
		}
	}
	d.Close() // nolint: errcheck
	return 0
}

func s3_uploader(start int, stop int, recordSize string) int {

	atomic.AddInt64(&stats.total_writes, int64(stop-start))
	defer writeGroup.Done()
	c.L.Lock()
	for !startRun {
		c.Wait()
	}
	c.L.Unlock()

	byteSize, err := humanize.ParseBytes(recordSize)
	if err != nil {
		panic(err)
	}

	svc := getService()

	payload := make([]byte, byteSize)

	for i := start; i < stop; i++ {
		// In case we need some pseudo-randomness
		if config.RandomData {
			if _, err := rand.Read(payload); err != nil { // nolint: staticcheck
				panic(err)
			}
		}
		params := &s3.PutObjectInput{
			Bucket: aws.String(config.Bucket),
			Key:    aws.String(recordSize + "/" + strconv.Itoa(i)),
			Body:   bytes.NewReader(payload),
		}
		_, err := svc.PutObject(context.TODO(), params)
		if err != nil {
			if config.AbortOnError {
				panic(err)
			}

			atomic.AddInt64(&stats.errors, 1)
			fmt.Println(err.Error())
		} else {

			atomic.AddInt64(&stats.writes, 1)
			atomic.AddInt64(&stats.bytes, int64(byteSize))

			// Log each Object
			if *logging {
				md := fmt.Sprintf("%x", md5.Sum(payload))
				logger.Printf("%v %v", config.Bucket+"/"+recordSize+"/"+strconv.Itoa(i), md)
			}
		}
	}
	return 0
}

func objectCount(bucketName string, recordSize string) int {
	svc := getService()

	truncated := true
	count := 1 // offset
	params := &s3.ListObjectsInput{
		Bucket:    aws.String(config.Bucket),
		Delimiter: aws.String("/"),
		MaxKeys:   aws.Int32(5000),
		Prefix:    aws.String(recordSize + "/"),
	}

	for truncated {
		resp, err := svc.ListObjects(context.TODO(), params)

		if err != nil {
			panic(err.Error())
		}

		if len(resp.Contents) == 0 {
			log.Println("bucket is empty")
			return 1
		}

		count += len(resp.Contents)

		if count >= config.ReadRange && !*stat {
			log.Println("found enough objects in the bucket for size:", recordSize)
			return 0
		}

		if *resp.IsTruncated {
			// TODODODODOODODDOO params.SetMarker(*resp.NextMarker)
			truncated = *resp.IsTruncated
		} else {
			truncated = false
		}

		/*
		 *  FALL THROUGH
		 */

	}

	if *stat {
		fmt.Printf("Found %d objects of size %s in bucket\n", count, recordSize)
		return 0
	}

	return 1
}

func stats_printer() {
	reads := atomic.LoadInt64(&stats.reads)
	writes := atomic.LoadInt64(&stats.writes)
	bytes := atomic.LoadInt64(&stats.bytes)
	time.Sleep(1 * time.Second)

	for startRun {
		log.Printf("queued/read: %d/%4d, queued/write: %6d/%4d, byte/s: %4s\n",
			uint64(math.Max(float64(atomic.LoadInt64(&stats.total_reads)-reads), 0)),
			atomic.LoadInt64(&stats.reads)-reads,
			uint64(math.Max(float64(atomic.LoadInt64(&stats.total_writes)-writes), 0)),
			atomic.LoadInt64(&stats.writes)-writes,
			humanize.IBytes(uint64(atomic.LoadInt64(&stats.bytes)-bytes)))
		reads = atomic.LoadInt64(&stats.reads)
		writes = atomic.LoadInt64(&stats.writes)
		bytes = atomic.LoadInt64(&stats.bytes)
		time.Sleep(1 * time.Second)
	}
}

func print_total() {

	elapsed := time.Since(then)
	fmt.Println("---")
	log.Printf("Elapsed time in seconds: %f", elapsed.Seconds())
	log.Printf("Total OPS: %d, operations per second: %d, bytes per second: %s",
		(stats.reads + stats.writes), uint64(float64(stats.reads+stats.writes)/elapsed.Seconds()),
		humanize.IBytes(uint64(float64(stats.bytes)/(elapsed.Seconds()))))

}

var stat = flag.Bool("stat", false, "stat target bucket and exit")
var filename = flag.String("c", "config.yaml", "YAML config file")
var help = flag.Bool("h", false, "need help")
var logging = flag.Bool("l", false, "Log putObject name/md5 to "+logFile)
var logger *log.Logger

func main() {

	ctrlc := make(chan os.Signal, 2)
	signal.Notify(ctrlc, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctrlc
		print_total()
		os.Exit(0)
	}()

	flag.Parse()

	if *help {
		fmt.Println("S3 Stress (S4) and Benchmark Swiss Army Knife")
		flag.PrintDefaults()
		return
	}

	//filename := os.Args[1]
	source, err := os.ReadFile(*filename)
	if err != nil {
		panic(err)
	}
	err = yaml.Unmarshal(source, &config)
	if err != nil {
		log.Fatalf("error: %v", err)
	}

	// See if we need to log things
	if *logging {
		f, err := os.Create(logFile)
		if err != nil {
			panic(err)
		}
		defer f.Close() // nolint: errcheck
		logger = log.New(f, "", log.Ldate|log.Ltime|log.Lmicroseconds)
	}

	runtime.GOMAXPROCS(1000)

	fmt.Printf("--- config:\n%v\n\n", config)

	for o := range config.Read {
		if objectCount(config.Bucket, config.Read[o].Recordsize) != 0 {
			panic("not enough objects in bucket of size")
		}
	}

	if *stat {
		return
	}

	startRun = false
	c.L.Lock()
	for i := 0; i < len(config.Write); i++ {
		for j := 0; j < config.Write[i].Threadcount; j++ {
			go s3_uploader(j*config.Write[i].Iterations,
				(j+1)*config.Write[i].Iterations, config.Write[i].Recordsize)
			writeGroup.Add(1)
		}

	}

	for i := 0; i < len(config.Read); i++ {
		for j := 0; j < config.Read[i].Threadcount; j++ {
			readGroup.Add(1)
			go s3_downloader(0, config.Read[i].Iterations, config.Read[i].Recordsize)
		}
		log.Printf("started: %d read thread(s)", config.Read[i].Threadcount)
	}

	startRun = true
	c.Broadcast()
	c.L.Unlock()

	go stats_printer()

	writeGroup.Wait()
	readGroup.Wait()

	print_total()

}

package main

import (
	"bytes"
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/dustin/go-humanize"
	"gopkg.in/yaml.v2"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"syscall"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"math"
	"runtime"
	"flag"
	"net/http"
	"net"
	"crypto/tls"
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
	S3_endpoint   string `yaml:"s3_endpoint"`
	tls_no_verify bool   `yaml:"tls_no_verify"`
	Bucket        string `yaml:"bucket"`
	ReadRange     int    `yaml:"read_range_max"`
	ReadSparse    bool   `yaml:"read_sparse"`
	Write         []Job  `yaml:"write"`
	Read          []Job  `yaml:"read"`
}

var stats Stats
var writeGroup sync.WaitGroup
var readGroup sync.WaitGroup
var running bool
var config Configs
var m sync.Mutex;

var c = sync.NewCond(&m)
var startRun bool
var then = time.Now()

func s3_downloader(start int, stop int, recordSize string) int {
	defer readGroup.Done()

	atomic.AddInt64(&stats.total_reads, int64(stop-start))
	c.L.Lock()
	for startRun == false {
		c.Wait()
	}
	c.L.Unlock()

	sess := session.New(&aws.Config{
		Endpoint:   aws.String(config.S3_endpoint),
		Region:     aws.String("region1"),
		DisableSSL: aws.Bool(true)})

	svc := s3.New(sess, &aws.Config{HTTPClient: &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: config.tls_no_verify},
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConnsPerHost:   100,
			TLSHandshakeTimeout:   3 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}})

	d, ferr := os.OpenFile("/dev/null", os.O_APPEND|os.O_WRONLY, os.ModeAppend)
	if ferr != nil {
		log.Fatal("Can not open output file")
		panic(ferr)
	}

	// collect object names
	params := &s3.ListObjectsInput{
	        Bucket:    aws.String(config.Bucket),
	        Delimiter: aws.String("/"),
	        MaxKeys:   aws.Int64(5000),
	        Prefix:    aws.String(recordSize + "/"),
	}
	resp, err := svc.ListObjects(params)
	if err != nil {
		panic(err.Error())
	}
	objects := make([]string, 1, config.ReadRange)
	for _, item := range(resp.Contents) {
		objects = append(objects, *item.Key)
	}

	for i := 0; i < stop; i++ {
		n := 1+rand.Intn(config.ReadRange)
		k := aws.String(objects[n])
		params := &s3.GetObjectInput{
			Bucket: aws.String(config.Bucket), // Required
			Key:    k,
		}
		resp, err := svc.GetObject(params)
		if err != nil {
			atomic.AddInt64(&stats.errors, 1)
			fmt.Println(err.Error())
		} else {
			if config.ReadSparse == false {
				// stream data to fh
				io.Copy(d, resp.Body)
			}
			atomic.AddInt64(&stats.bytes, *resp.ContentLength)
			atomic.AddInt64(&stats.reads, 1)
			resp.Body.Close()
		}
	}
	d.Close()
	return 0
}

func s3_uploader(start int, stop int, recordSize string) int {

	atomic.AddInt64(&stats.total_writes, int64(stop-start))
	defer writeGroup.Done()
	c.L.Lock()
	for startRun == false {
		c.Wait()
	}
	c.L.Unlock()

	byteSize, err := humanize.ParseBytes(recordSize)
	if err != nil {
		panic(err)
	}


	sess := session.New(&aws.Config{
		Endpoint:   aws.String(config.S3_endpoint),
		Region:     aws.String("region1"),
		DisableSSL: aws.Bool(true)})

	svc := s3.New(sess, &aws.Config{HTTPClient: &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: config.tls_no_verify},
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConnsPerHost:   100,
			TLSHandshakeTimeout:   3 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}})

	payload := make([]byte, byteSize, byteSize)

	for i := start; i < stop; i++ {
		params := &s3.PutObjectInput{
			Bucket: aws.String(config.Bucket),
			Key:    aws.String(recordSize + "/" + strconv.Itoa(i)),
			Body:   bytes.NewReader(payload),
		}
		_, err := svc.PutObject(params)
		if err != nil {
			atomic.AddInt64(&stats.errors, 1)
			fmt.Println(err.Error())
		} else {

			atomic.AddInt64(&stats.writes, 1)
			atomic.AddInt64(&stats.bytes, int64(byteSize))
		}

	}
	return 0
}

func objectCount(bucketName string, recordSize string) int {
	sess := session.New(&aws.Config{
		Endpoint:   aws.String(config.S3_endpoint),
		Region:     aws.String("region1"),
		DisableSSL: aws.Bool(true)})

	svc := s3.New(sess)
	truncated := true
	count := 1 // offset
	params := &s3.ListObjectsInput{
		Bucket:    aws.String(config.Bucket),
		Delimiter: aws.String("/"),
		MaxKeys:   aws.Int64(5000),
		Prefix:    aws.String(recordSize + "/"),
	}

	for truncated == true {

		resp, err := svc.ListObjects(params)

		if err != nil {
			panic(err.Error())
		}

		if len(resp.Contents) == 0 {
			log.Println("boeket is empty")
			return 1
		}

		count += len(resp.Contents)

		if count >= config.ReadRange && *stat == false {
			log.Println("found enough objects in the boeket for size:", recordSize)
			return 0
		}

		if *resp.IsTruncated == true {
			params.SetMarker(*resp.NextMarker)
			truncated = *resp.IsTruncated
		} else {
			truncated = false
		}

		/*
		 *  FALL THROUGH
		 */

	}

	if *stat == true {
		fmt.Printf("Found %d objects of size %s in bucket\n", count, recordSize)
		return 0
	}

	return 1
}

func stats_printer() {

	var reads, bytes, writes int64
	reads = atomic.LoadInt64(&stats.reads)
	reads = atomic.LoadInt64(&stats.writes)
	bytes = atomic.LoadInt64(&stats.bytes)
	time.Sleep(1 * time.Second)

	for startRun == true {
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
		(stats.reads + stats.writes), (stats.reads+stats.writes)/int64(elapsed.Seconds()),
		humanize.IBytes(uint64(stats.bytes/(int64(elapsed.Seconds())))))

}

var stat = flag.Bool("stat", false, "stat target bucket and exit")
var filename = flag.String("c", "config.yaml", "YAML config file")
var help = flag.Bool("h", false, "need help")

func main() {

	ctrlc := make(chan os.Signal, 2)
	signal.Notify(ctrlc, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctrlc
		print_total()
		os.Exit(0)
		return
	}()

	flag.Parse()

	if *help == true {
		fmt.Println("S3 Stress (S4) and Benchmark Swiss Army Knife")
		flag.PrintDefaults()
		return
	}

	rand.Seed(time.Now().UnixNano())

	//filename := os.Args[1]
	source, err := ioutil.ReadFile(*filename)
	if err != nil {
		panic(err)
	}
	err = yaml.Unmarshal(source, &config)
	if err != nil {
		log.Fatalf("error: %v", err)
	}

	runtime.GOMAXPROCS(1000)

	fmt.Printf("--- config:\n%v\n\n", config)

	for o := range config.Read {
		if objectCount(config.Bucket, config.Read[o].Recordsize) != 0 {
			panic("not enough objects in bucket of size")
		}
	}

	if *stat == true {
		return
	}

	startRun = false;
	c.L.Lock();
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
	running = false

	print_total()

}

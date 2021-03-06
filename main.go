package main

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/visualfc/goqt/ui"
)

const (
	seglen = 1452
	N      = 32
)

var chRow chan rowinfo
var chURL chan string
var chDir chan urldir
var chMsg chan string
var chTask chan *taskinfo
var chMrg chan string

//var chSig chan os.Signal
var threads int32 = 5
var automerge bool = true
var merger string
var autodel bool = true
var container = "Original"
var cindex int32 = 0
var dirs DirArray

func loadConfig(file string) {
	var config Config
	data, err := ioutil.ReadFile(file)
	if err != nil {
		return
	}
	err = json.Unmarshal(data, &config)
	if err != nil {
		return
	}
	threads = config.Threads
	automerge = config.Automerge
	autodel = config.Autodel
	cindex = config.CIndex
	dirs = config.Dirs
}

func dumpConfig(file string) {
	config := Config{threads, automerge, autodel, cindex, dirs}
	data, _ := json.MarshalIndent(config, "", "    ")
	ioutil.WriteFile(file, data, 0644)
}

func init() {
	chRow = make(chan rowinfo, N)
	chURL = make(chan string, N)
	chDir = make(chan urldir, N)
	chMsg = make(chan string, N)
	chTask = make(chan *taskinfo, N)
	chMrg = make(chan string, N)
	//chSig = make(chan os.Signal, N)
}

func main() {
	var url string
	//signal.Notify(chSig, syscall.SIGCHLD)
	if len(os.Args) > 1 && strings.HasPrefix(os.Args[1], "bigrats://") {
		url = "http://www.flvcd.com/diy/" + os.Args[1][10:] + ".htm"
	}
	uid := os.Getuid()
	addr := "/tmp/bigrats:" + strconv.Itoa(uid)
listen:
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{addr, "unixgram"})
	if err != nil {
		conn, err = net.DialUnix("unixgram", nil, &net.UnixAddr{addr, "unixgram"})
		if err != nil {
			log.Println(err)
			os.Remove(addr)
			goto listen
		}
		_, err = conn.Write([]byte(url))
		if err != nil {
			log.Println(err)
		}
		return
	}
	defer os.Remove(addr)

	user, err := user.Current()
	if err == nil {
		cfgfile := filepath.Join(user.HomeDir, ".bigrats")
		loadConfig(cfgfile)
		defer dumpConfig(cfgfile)
	}

	go func() {
		for {
			msg := make([]byte, 256)
			n, err := conn.Read(msg)
			if err != nil {
				log.Println(err)
				continue
			}
			runTask(string(msg[:n]))
		}
	}()
	go runTask(url)
	go scheduler()

	ui.RunEx(os.Args, gui)
}

func runTask(url string) {
	chURL <- url
	tmp := <-chDir
	url = tmp.url
	dir := tmp.dir
	if url == "" || dir == "" {
		return
	}
	task, err := parseURL(url)
	if err != nil {
		chMsg <- err.Error()
		return
	}
	if !strings.HasSuffix(dir, "/") {
		dir += "/"
	}
	task.dir = dir
	chTask <- task
}

func scheduler() {
	var tasks []*taskinfo
	var active int32
	var joiner *mrgtool

	feedback := make(chan backinfo, N)
	sched := func() {
		if active >= threads {
			return
		}
		for _, task := range tasks {
			for _, seg := range task.segs {
				if seg.status != READY {
					continue
				}
				go fetchSegment(seg, feedback)
				seg.status = DOWN
				active++
				if active >= threads {
					return
				}
			}
		}
	}
	for {
		select {
		case t := <-chTask:
			if t != nil {
				exist := false
				for _, task := range tasks {
					if t.tid == task.tid {
						exist = true
						chMsg <- "This video is already in task list."
						break
					}
				}
				if !exist {
					for _, seg := range t.segs {
						chRow <- rowinfo{seg, -1, 0, -1, "-", "Waiting"}
					}
					tasks = append(tasks, t)
				}
			}
			sched()
		case b := <-feedback:
			b.seg.status = b.status
			if b.status == DONE || b.status == ERROR {
				active--
				sched()
			}
			if b.status == DONE {
				var seg *seginfo
				merge := true
				for _, seg = range b.seg.task.segs {
					if seg.status != DONE {
						merge = false
						break
					}
				}
				if !merge {
					break
				}
				if automerge {
					chMrg <- "Merging " + seg.task.title
					if joiner == nil {
						joiner = new(mrgtool)
						wrapper := filepath.Dir(os.Args[0]) + "/wrapper"
						_, err := os.Stat(wrapper)
						if err != nil {
							chMrg <- err.Error()
							break
						}
						cmd := exec.Command(wrapper, merger)
						joiner.wr, _ = cmd.StdinPipe()
						joiner.rd, _ = cmd.StdoutPipe()
						joiner.err, _ = cmd.StderrPipe()
						err = cmd.Start()
						if err != nil {
							chMrg <- err.Error()
							break
						}
					}
					err := mergeSegs(seg.task, container, joiner, autodel)
					if err != nil {
						chMrg <- err.Error()
						break
					} else {
						chMrg <- ""
					}

					/*if autodel && (len(seg.task.segs) > 1 || container != "Original") {
						go func() {
							err := delSegs(seg.task, container)
							if err != nil {
								chMrg <- err.Error()
							}
						}()
					}*/
				}
			}
		}
	}
}

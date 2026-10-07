package cli

import (
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
	"sync"
	"time"

	"github.com/spf13/cobra"
)

// Profiler writes pprof profiles for one run, for finding where a build spends its memory
// and time. The heap profile is the one taken at the run's highest heap, not at its end: a
// build's peak is what has to fit in a machine, and by the time it exits most of what made
// the peak has been freed.
type Profiler struct {
	cpuPath, memPath string

	cpu  *os.File
	stop chan struct{}
	done sync.WaitGroup
	peak uint64
}

func (p *Profiler) Register(cmd *cobra.Command) {
	pf := cmd.PersistentFlags()
	pf.StringVar(&p.cpuPath, "cpuprofile", "", "write a CPU profile of the run to this file (go tool pprof)")
	pf.StringVar(&p.memPath, "memprofile", "", "write a heap profile taken at the run's peak heap to this file (go tool pprof); the peak is also logged")
}

func (p *Profiler) Start() error {
	if p.cpuPath != "" {
		f, err := os.Create(p.cpuPath)
		if err != nil {
			return err
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			f.Close()
			return err
		}
		p.cpu = f
	}
	if p.memPath != "" {
		p.stop = make(chan struct{})
		p.done.Add(1)
		go p.watch()
	}
	return nil
}

// watch samples the heap and rewrites the profile each time it reaches a new high. A
// snapshot costs a stop-the-world read, so new highs are only taken when they are 5% above
// the last, which keeps a steadily growing build from profiling itself continuously.
func (p *Profiler) watch() {
	defer p.done.Done()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-tick.C:
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			if ms.HeapInuse > p.peak+p.peak/20 {
				p.peak = ms.HeapInuse
				p.write()
			}
		}
	}
}

func (p *Profiler) write() {
	f, err := os.Create(p.memPath + ".writing")
	if err != nil {
		return
	}
	// The "inuse" sample index of a heap profile shows what is live at this moment.
	if err := pprof.Lookup("heap").WriteTo(f, 0); err == nil && f.Close() == nil {
		os.Rename(p.memPath+".writing", p.memPath)
	}
}

func (p *Profiler) Finish() {
	if p.cpu != nil {
		pprof.StopCPUProfile()
		p.cpu.Close()
	}
	if p.stop != nil {
		close(p.stop)
		p.done.Wait()
		fmt.Fprintf(Gate.LogWriter(), "heap profile at peak heap in use %.2f GB: %s\n", float64(p.peak)/1e9, p.memPath)
	}
}

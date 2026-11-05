package recovery

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"
)

// BinlogFile : journal binaire trouvé sur le disque.
type BinlogFile struct {
	Name     string `json:"name"`
	Bytes    int64  `json:"bytes"`
	First    string `json:"first,omitempty"`
	Last     string `json:"last,omitempty"`
	LastGTID string `json:"lastGtid,omitempty"`
	Events   int    `json:"transactions"`
	Error    string `json:"error,omitempty"`
}

var (
	binlogEventRe = regexp.MustCompile(`^#(\d{6})\s+(\d{1,2}:\d{2}:\d{2}) server id \d+`)
	binlogGTIDRe  = regexp.MustCompile(`\tGTID (\d+-\d+-\d+)`)
)

// binlogs copie les journaux binaires du serveur mort dans out/binlogs et les résume.
func (j *job) binlogs(ctx context.Context) {
	if len(j.inv.Binlogs) == 0 {
		return
	}
	dir := filepath.Join(j.o.Out, "binlogs")
	_ = os.MkdirAll(dir, 0o750)
	var copied []string
	for _, name := range j.inv.Binlogs {
		bf := BinlogFile{Name: name}
		src := filepath.Join(j.o.Source, name)
		if st, err := os.Stat(src); err == nil {
			bf.Bytes = st.Size()
		}
		dst := filepath.Join(dir, name)
		if err := copyFile(src, dst); err != nil {
			bf.Error = "copie impossible : " + err.Error()
			j.r.Binlogs = append(j.r.Binlogs, bf)
			continue
		}
		copied = append(copied, dst)
		if j.t != nil && j.t.Binlog != "" {
			summarize(ctx, j.t.Binlog, dst, &bf)
		}
		j.r.Binlogs = append(j.r.Binlogs, bf)
	}
	j.log("%d journaux binaires copiés dans %s", len(copied), dir)
	if j.o.BinlogsSince != "" && j.t != nil && j.t.Binlog != "" && len(copied) > 0 {
		if _, err := time.Parse("2006-01-02 15:04:05", j.o.BinlogsSince); err != nil {
			j.r.Repairs = append(j.r.Repairs, "date -binlogs-depuis invalide (attendu AAAA-MM-JJ HH:MM:SS)")
			return
		}
		out := filepath.Join(j.o.Out, "binlogs-depuis.sql")
		f, err := os.Create(out)
		if err != nil {
			return
		}
		args := append([]string{"--no-defaults", "--start-datetime=" + j.o.BinlogsSince}, copied...)
		err = runCmd(ctx, f, j.t.Binlog, args...)
		f.Close()
		if err != nil {
			j.r.Repairs = append(j.r.Repairs, "export SQL des binlogs impossible : "+short(err.Error()))
			_ = os.Remove(out)
			return
		}
		j.r.BinlogSQL = "binlogs-depuis.sql"
	}
}

// summarize lit un binlog : première et dernière date, dernier GTID, nombre de transactions.
func summarize(ctx context.Context, tool, file string, bf *BinlogFile) {
	cmd := exec.CommandContext(ctx, tool, "--no-defaults", file)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	if err := cmd.Start(); err != nil {
		bf.Error = err.Error()
		return
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		l := sc.Text()
		if len(l) < 8 || l[0] != '#' {
			continue
		}
		m := binlogEventRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		ts := fmt.Sprintf("20%s-%s-%s %s", m[1][0:2], m[1][2:4], m[1][4:6], m[2])
		if bf.First == "" {
			bf.First = ts
		}
		bf.Last = ts
		if g := binlogGTIDRe.FindStringSubmatch(l); g != nil {
			bf.LastGTID = g[1]
			bf.Events++
		}
	}
	if err := cmd.Wait(); err != nil && bf.Last == "" {
		bf.Error = "lecture impossible (fichier tronqué ?)"
	}
}

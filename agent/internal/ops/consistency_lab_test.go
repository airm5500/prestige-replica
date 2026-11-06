package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/airm5500/prestige-replica-manager/agent/internal/config"
)

type printRep struct{ t *testing.T }

func (r printRep) Step(m string) { r.t.Log("étape : " + m) }
func (r printRep) Warn(m string) { r.t.Log("ALERTE : " + m) }

// Essai sur le laboratoire local (primaire 3308, réplique 3307) : PRM_LAB_CHECK=1 go test -run Lab.
func TestLabConsistency(t *testing.T) {
	if os.Getenv("PRM_LAB_CHECK") == "" {
		t.Skip("laboratoire MariaDB non demandé")
	}
	cfg := &config.Config{}
	cfg.MariaDB.Address = "127.0.0.1:3307"
	cfg.ConsistencyCheck = config.ConsistencyCheckConfig{Enabled: true, User: "prm_check", PasswordFile: "/tmp/e2e2/check.pw"}
	r := &Runner{Cfg: cfg}
	res, err := r.CheckConsistency(context.Background(), ConsistencyParams{SourceHost: "127.0.0.1", SourcePort: 3308, Mode: "FULL",
		ChunkRows: 1000, PauseFactor: 0.5, Rechecks: 2}, printRep{t})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.MarshalIndent(res, "", " ")
	fmt.Println(string(b))
}

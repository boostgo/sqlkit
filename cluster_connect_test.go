package sqlkit

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"gopkg.in/yaml.v3"
)

// spyDialer hands out sqlmock pools and records the configs it was asked to open.
type spyDialer struct {
	t           *testing.T
	pm          sqlmock.Sqlmock
	pc          *sqlx.DB
	rms         []sqlmock.Sqlmock
	rcs         []*sqlx.DB
	primaryCfg  []Config
	replicaCfgs []Config
	primaryErr  error
	replicaErr  map[int]error // by the call number
}

func newSpyDialer(t *testing.T, replicas int) *spyDialer {
	t.Helper()

	pc, pm := newMockDB(t)
	d := &spyDialer{t: t, pc: pc, pm: pm, replicaErr: map[int]error{}}

	for range replicas {
		rc, rm := newMockDB(t)
		d.rcs, d.rms = append(d.rcs, rc), append(d.rms, rm)
	}

	return d
}

func (d *spyDialer) dialer() clusterDialer {
	return clusterDialer{
		primary: func(cfg Config) (*sqlx.DB, error) {
			d.primaryCfg = append(d.primaryCfg, cfg)

			if d.primaryErr != nil {
				return nil, d.primaryErr
			}

			return d.pc, nil
		},
		replica: func(cfg Config) (*sqlx.DB, error) {
			n := len(d.replicaCfgs)
			d.replicaCfgs = append(d.replicaCfgs, cfg)

			if err := d.replicaErr[n]; err != nil {
				return nil, err
			}

			return d.rcs[n], nil
		},
	}
}

// expectFirstCheck makes the lag check on start succeed for the first n replicas.
func (d *spyDialer) expectFirstCheck(n int) {
	expectPrimaryLSN(d.pm)

	for i := range n {
		expectReplica(d.rms[i], false, nil, nil)
	}
}

// waitChecked waits until the first lag check of every replica is done.
func waitChecked(t *testing.T, c *Cluster) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)

	for {
		done := true

		for _, s := range c.States() {
			done = done && s.Reason != ReasonNotChecked
		}

		if done {
			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("no first check: %+v", c.States())
		}

		time.Sleep(2 * time.Millisecond)
	}
}

// expectClose expects Close of every pool: the replicas first, then the primary.
func (d *spyDialer) expectClose() {
	for _, rm := range d.rms {
		rm.ExpectClose()
	}

	d.pm.ExpectClose()
}

func fullPrimary() Config {
	return Config{
		Host: "primary", Port: 5432, Username: "u", Password: "p", Database: "d", TimeoutMs: 1500,
		MigrationHost: "mh", MigrationPort: 1, MigrationUsername: "mu", MigrationPassword: "mp", MigrationDatabase: "md",
		BinaryParameters: true, MaxOpenConnections: 11, MaxIdleConnections: 5, MaxLifetime: 600, MaxIdleTime: 60,
		ReadTimeout: 7, WriteTimeout: 8, Driver: PgxDriver, SSLMode: "require", ClickhouseScheme: "https",
	}
}

func TestInheritReplicaConfig(t *testing.T) {
	primary := fullPrimary()

	t.Run("empty replica takes everything from primary except Host, Migration*, ClickhouseScheme", func(t *testing.T) {
		got := inheritReplicaConfig(primary, Config{Host: "r1"})

		want := primary
		want.Host = "r1"
		want.MigrationHost, want.MigrationPort, want.MigrationUsername = "", 0, ""
		want.MigrationPassword, want.MigrationDatabase, want.ClickhouseScheme = "", "", ""

		if got != want {
			t.Fatalf("got  %+v\nwant %+v", got, want)
		}
	})

	// every inheritable field: set in the replica -> its own value wins
	overrides := map[string]func(c *Config){
		"Port":               func(c *Config) { c.Port = 6432 },
		"Username":           func(c *Config) { c.Username = "ru" },
		"Password":           func(c *Config) { c.Password = "rp" },
		"Database":           func(c *Config) { c.Database = "rd" },
		"Driver":             func(c *Config) { c.Driver = PqDriver },
		"SSLMode":            func(c *Config) { c.SSLMode = "verify-full" },
		"TimeoutMs":          func(c *Config) { c.TimeoutMs = 99 },
		"MaxOpenConnections": func(c *Config) { c.MaxOpenConnections = 2 },
		"MaxIdleConnections": func(c *Config) { c.MaxIdleConnections = 1 },
		"MaxLifetime":        func(c *Config) { c.MaxLifetime = 5 },
		"MaxIdleTime":        func(c *Config) { c.MaxIdleTime = 6 },
		"ReadTimeout":        func(c *Config) { c.ReadTimeout = 1 },
		"WriteTimeout":       func(c *Config) { c.WriteTimeout = 2 },
	}

	for field, set := range overrides {
		t.Run("own "+field, func(t *testing.T) {
			rc := Config{Host: "r1"}
			set(&rc)

			got := inheritReplicaConfig(primary, rc)
			if !reflect.DeepEqual(reflect.ValueOf(got).FieldByName(field).Interface(),
				reflect.ValueOf(rc).FieldByName(field).Interface()) {
				t.Fatalf("%s was not taken from the replica: %+v", field, got)
			}

			// all other inheritable fields still come from primary
			for other, set2 := range overrides {
				if other == field {
					continue
				}

				probe := Config{}
				set2(&probe)

				if reflect.ValueOf(got).FieldByName(other).Interface() ==
					reflect.ValueOf(probe).FieldByName(other).Interface() {
					t.Fatalf("%s leaked from the replica", other)
				}
			}
		})
	}

	t.Run("BinaryParameters", func(t *testing.T) {
		if !inheritReplicaConfig(primary, Config{Host: "r"}).BinaryParameters {
			t.Fatal("must be inherited")
		}

		if !inheritReplicaConfig(Config{}, Config{Host: "r", BinaryParameters: true}).BinaryParameters {
			t.Fatal("own value must be kept")
		}
	})
}

func TestConnectClusterOpensAndClosesEverything(t *testing.T) {
	d := newSpyDialer(t, 2)
	d.expectFirstCheck(2)
	d.expectClose()

	cfg := ClusterConfig{
		Primary:         fullPrimary(),
		ReplicasEnabled: true,
		Replicas: []ReplicaConfig{
			{Name: "a", Config: Config{Host: "ra"}},
			{Name: "b", Config: Config{Host: "rb", Port: 6432, Database: "other"}},
		},
		MaxLagSeconds:           10,
		LagCheckIntervalSeconds: 3600,
	}

	c, err := connectCluster(cfg, d.dialer())
	if err != nil {
		t.Fatal(err)
	}

	waitChecked(t, c)

	if len(d.replicaCfgs) != 2 || d.replicaCfgs[0].Host != "ra" || d.replicaCfgs[0].Port != 5432 ||
		d.replicaCfgs[1].Port != 6432 || d.replicaCfgs[1].Database != "other" || d.replicaCfgs[1].Username != "u" {
		t.Fatalf("replica configs: %+v", d.replicaCfgs)
	}

	if c.cfg.maxLag != 10*time.Second || c.cfg.interval != time.Hour {
		t.Fatalf("lag options from config: %+v", c.cfg)
	}

	states := c.States()
	if len(states) != 2 || states[0].Name != "a" || states[1].Name != "b" {
		t.Fatalf("%+v", states)
	}

	// Close closes the replicas and then the primary
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}

	if err = c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	waitMet(t, d.pm)
	waitMet(t, d.rms[0])
	waitMet(t, d.rms[1])
}

func TestConnectClusterCloseJoinsErrors(t *testing.T) {
	d := newSpyDialer(t, 1)
	d.expectFirstCheck(1)

	errR, errP := errors.New("replica close"), errors.New("primary close")
	d.rms[0].ExpectClose().WillReturnError(errR)
	d.pm.ExpectClose().WillReturnError(errP)

	c, err := connectCluster(ClusterConfig{
		Primary: fullPrimary(), ReplicasEnabled: true, LagCheckIntervalSeconds: 3600,
		Replicas: []ReplicaConfig{{Name: "a", Config: Config{Host: "ra"}}},
	}, d.dialer())
	if err != nil {
		t.Fatal(err)
	}

	waitChecked(t, c)

	if err = c.Close(); !errors.Is(err, errR) || !errors.Is(err, errP) {
		t.Fatalf("%v", err)
	}

	if err = c.Close(); err != nil {
		t.Fatalf("second Close must be a no-op: %v", err)
	}
}

func TestConnectClusterReplicasDisabled(t *testing.T) {
	d := newSpyDialer(t, 1)

	c, err := connectCluster(ClusterConfig{
		Primary:  fullPrimary(),
		Replicas: []ReplicaConfig{{Name: "a", Config: Config{Host: "ra"}}},
	}, d.dialer())
	if err != nil {
		t.Fatal(err)
	}

	if len(d.replicaCfgs) != 0 {
		t.Fatal("replicas must not be opened")
	}

	if st := c.State(); st.Reason != ReasonNoReplica || len(c.States()) != 0 {
		t.Fatalf("%+v", st)
	}

	d.pm.ExpectClose()

	if err = c.Close(); err != nil {
		t.Fatal(err)
	}

	waitMet(t, d.pm)
}

func TestConnectClusterValidation(t *testing.T) {
	ok := ReplicaConfig{Name: "a", Config: Config{Host: "ra"}}

	cases := map[string]ClusterConfig{
		"bad primary driver":   {Primary: Config{Driver: "mysql"}},
		"clickhouse primary":   {Primary: Config{Driver: ChDriver}},
		"bad replica driver":   {ReplicasEnabled: true, Replicas: []ReplicaConfig{{Name: "a", Config: Config{Host: "h", Driver: "mysql"}}}},
		"empty replica host":   {ReplicasEnabled: true, Replicas: []ReplicaConfig{{Name: "a"}}},
		"empty replica name":   {ReplicasEnabled: true, Replicas: []ReplicaConfig{{Config: Config{Host: "h"}}}},
		"duplicate name":       {ReplicasEnabled: true, Replicas: []ReplicaConfig{ok, ok}},
		"negative max lag":     {MaxLagSeconds: -1},
		"negative interval":    {LagCheckIntervalSeconds: -1},
		"negative with enable": {ReplicasEnabled: true, Replicas: []ReplicaConfig{ok}, MaxLagSeconds: -5},
	}

	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			d := newSpyDialer(t, 1)

			c, err := connectCluster(cfg, d.dialer())
			if !errors.Is(err, ErrClusterOption) || c != nil {
				t.Fatalf("%v", err)
			}

			if len(d.primaryCfg) != 0 || len(d.replicaCfgs) != 0 {
				t.Fatal("nothing must be opened on a config error")
			}
		})
	}

	t.Run("empty and pgx drivers pass, invalid replicas ignored when disabled", func(t *testing.T) {
		for _, drv := range []string{"", PqDriver, PgxDriver} {
			if err := validateClusterConfig(ClusterConfig{Primary: Config{Driver: drv}}); err != nil {
				t.Fatalf("%q: %v", drv, err)
			}
		}

		if err := validateClusterConfig(ClusterConfig{Replicas: []ReplicaConfig{{}}}); err != nil {
			t.Fatalf("disabled replicas must not be validated: %v", err)
		}
	})

	t.Run("public ConnectCluster validates before any connect", func(t *testing.T) {
		if _, err := ConnectCluster(ClusterConfig{Primary: Config{Driver: "mysql"}}); !errors.Is(err, ErrClusterOption) {
			t.Fatalf("%v", err)
		}
	})
}

func TestConnectClusterPrimaryError(t *testing.T) {
	d := newSpyDialer(t, 1)
	d.primaryErr = ErrPing

	c, err := connectCluster(ClusterConfig{
		Primary: fullPrimary(), ReplicasEnabled: true,
		Replicas: []ReplicaConfig{{Name: "a", Config: Config{Host: "ra"}}},
	}, d.dialer())
	if !errors.Is(err, ErrPing) || c != nil {
		t.Fatalf("%v", err)
	}

	if len(d.replicaCfgs) != 0 {
		t.Fatal("replicas must not be opened after a primary error")
	}
}

func TestConnectClusterReplicaOpenErrorClosesEverything(t *testing.T) {
	d := newSpyDialer(t, 3)
	d.replicaErr[2] = ErrOpenConnect

	// closed: primary and the two replicas that were opened
	d.pm.ExpectClose()
	d.rms[0].ExpectClose()
	d.rms[1].ExpectClose()

	c, err := connectCluster(ClusterConfig{
		Primary: fullPrimary(), ReplicasEnabled: true,
		Replicas: []ReplicaConfig{
			{Name: "a", Config: Config{Host: "ra"}}, {Name: "b", Config: Config{Host: "rb"}}, {Name: "c", Config: Config{Host: "rc"}},
		},
	}, d.dialer())
	if !errors.Is(err, ErrOpenConnect) || c != nil {
		t.Fatalf("%v", err)
	}

	waitMet(t, d.pm)
	waitMet(t, d.rms[0])
	waitMet(t, d.rms[1])
}

func TestConnectClusterCallerOptions(t *testing.T) {
	t.Run("caller options win", func(t *testing.T) {
		d := newSpyDialer(t, 1)
		d.expectFirstCheck(1)
		d.expectClose()

		balancer := LeastLag()

		c, err := connectCluster(ClusterConfig{
			Primary: fullPrimary(), ReplicasEnabled: true, MaxLagSeconds: 10, LagCheckIntervalSeconds: 3600,
			Replicas: []ReplicaConfig{{Name: "a", Config: Config{Host: "ra"}}},
		}, d.dialer(), WithMaxLag(time.Minute), WithReplicaBalancer(balancer), WithRecoveryConflictRetry(false))
		if err != nil {
			t.Fatal(err)
		}

		waitChecked(t, c)

		if c.cfg.maxLag != time.Minute || c.cfg.conflictRetry {
			t.Fatalf("%+v", c.cfg)
		}

		if err = c.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a caller replica replaces the config one named default", func(t *testing.T) {
		d := newSpyDialer(t, 1)
		extra, extraMock := newMockDB(t)

		expectPrimaryLSN(d.pm)
		expectReplica(extraMock, false, nil, nil)
		d.pm.ExpectClose()
		d.rms[0].ExpectClose() // opened from the config: still owned, closed by Close

		c, err := connectCluster(ClusterConfig{
			Primary: fullPrimary(), ReplicasEnabled: true, LagCheckIntervalSeconds: 3600,
			Replicas: []ReplicaConfig{{Name: "default", Config: Config{Host: "ra"}}},
		}, d.dialer(), WithReplica(extra))
		if err != nil {
			t.Fatal(err)
		}

		if len(c.replicas) != 1 || c.replicas[0].conn != extra {
			t.Fatal("WithReplica must replace the replica from the config")
		}

		waitChecked(t, c)

		if err = c.Close(); err != nil {
			t.Fatal(err)
		}

		waitMet(t, d.rms[0])
		waitMet(t, d.pm)
	})

	t.Run("an invalid caller option closes everything", func(t *testing.T) {
		d := newSpyDialer(t, 1)
		d.rms[0].ExpectClose()
		d.pm.ExpectClose()

		c, err := connectCluster(ClusterConfig{
			Primary: fullPrimary(), ReplicasEnabled: true,
			Replicas: []ReplicaConfig{{Name: "a", Config: Config{Host: "ra"}}},
		}, d.dialer(), WithReplicaBalancer(nil))
		if !errors.Is(err, ErrClusterOption) || c != nil {
			t.Fatalf("%v", err)
		}

		waitMet(t, d.pm)
		waitMet(t, d.rms[0])
	})

	t.Run("a duplicate name from a caller option closes everything", func(t *testing.T) {
		d := newSpyDialer(t, 1)
		extra, _ := newMockDB(t)
		d.rms[0].ExpectClose()
		d.pm.ExpectClose()

		_, err := connectCluster(ClusterConfig{
			Primary: fullPrimary(), ReplicasEnabled: true,
			Replicas: []ReplicaConfig{{Name: "a", Config: Config{Host: "ra"}}},
		}, d.dialer(), WithNamedReplica("a", extra))
		if !errors.Is(err, ErrClusterOption) {
			t.Fatalf("%v", err)
		}

		waitMet(t, d.pm)
		waitMet(t, d.rms[0])
	})
}

// freePort returns a TCP port nobody listens on.
func freePort(t *testing.T) int {
	t.Helper()

	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	port := l.Addr().(*net.TCPAddr).Port

	if err = l.Close(); err != nil {
		t.Fatal(err)
	}

	return port
}

// An unreachable replica (real driver, a port with no server) must not stop the start; it ends up in check_failed.
func TestConnectClusterUnreachableReplicaDoesNotStopStart(t *testing.T) {
	d := newSpyDialer(t, 0)
	d.expectFirstCheck(0)

	dial := d.dialer()
	dial.replica = openFromConfig // the real opener, without ping

	changed := make(chan ReplicaState, 4)

	c, err := connectCluster(ClusterConfig{
		Primary:         Config{Host: "127.0.0.1", Port: 1, Username: "u", Password: "p", Database: "d", Driver: PqDriver, TimeoutMs: 500},
		ReplicasEnabled: true, LagCheckIntervalSeconds: 3600,
		Replicas: []ReplicaConfig{{Name: "down", Config: Config{Host: "127.0.0.1", Port: freePort(t)}}},
	}, dial, WithStateHook(func(s ReplicaState) { changed <- s }))
	if err != nil {
		t.Fatalf("an unreachable replica must not fail the start: %v", err)
	}

	select {
	case s := <-changed:
		if s.Name != "down" || s.Usable || s.Reason != ReasonCheckFailed {
			t.Fatalf("%+v", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no check_failed")
	}

	// reads with PreferReplica go to primary; with RequireReplica they fail
	d.pm.ExpectQuery("SELECT").WillReturnRows(rowsA())

	var dst int
	if err = c.GetContext(PreferReplica(context.Background()), &dst, "SELECT a"); err != nil {
		t.Fatal(err)
	}

	if err = c.GetContext(RequireReplica(context.Background()), &dst, "SELECT a"); !errors.Is(err, ErrReplicaUnavailable) {
		t.Fatalf("%v", err)
	}

	d.pm.ExpectClose()

	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenFromConfigDoesNotPing(t *testing.T) {
	db, err := openFromConfig(Config{
		Host: "127.0.0.1", Port: freePort(t), Username: "u", Password: "p", Database: "d",
		MaxOpenConnections: 3, MaxIdleConnections: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = db.Close() }()

	if got := db.Stats().MaxOpenConnections; got != 3 {
		t.Fatalf("pool options must be applied, MaxOpenConnections = %d", got)
	}
}

// The fields of ReplicaConfig are flat in YAML (inline) and in JSON (embedded without a tag).
func TestReplicaConfigFlatYAMLAndJSON(t *testing.T) {
	const doc = `
primary:
  host: p
  port: 5432
replicasEnabled: true
maxLagSeconds: 20
lagCheckIntervalSeconds: 3
replicas:
  - name: r1
    host: h1
    port: 6432
    maxOpenConnections: 4
  - name: r2
    host: h2
`

	var cfg ClusterConfig
	if err := yaml.Unmarshal([]byte(doc), &cfg); err != nil {
		t.Fatal(err)
	}

	if !cfg.ReplicasEnabled || cfg.MaxLagSeconds != 20 || cfg.LagCheckIntervalSeconds != 3 || len(cfg.Replicas) != 2 ||
		cfg.Primary.Host != "p" {
		t.Fatalf("%+v", cfg)
	}

	r1 := cfg.Replicas[0]
	if r1.Name != "r1" || r1.Host != "h1" || r1.Port != 6432 || r1.MaxOpenConnections != 4 || cfg.Replicas[1].Host != "h2" {
		t.Fatalf("yaml replica fields must be on one level: %+v", r1)
	}

	var fromJSON ClusterConfig

	const js = `{"primary":{"host":"p"},"replicas_enabled":true,"max_lag_seconds":20,"lag_check_interval_seconds":3,
		"replicas":[{"name":"r1","host":"h1","port":6432,"max_open_connections":4},{"name":"r2","host":"h2"}]}`
	if err := json.Unmarshal([]byte(js), &fromJSON); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(fromJSON.Replicas[0], r1) || fromJSON.Replicas[1].Host != "h2" || !fromJSON.ReplicasEnabled {
		t.Fatalf("json: %+v", fromJSON)
	}
}

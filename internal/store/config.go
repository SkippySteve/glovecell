package store

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config is how the controller reaches PostgreSQL. URL comes from a Secret:
// the CloudNativePG-generated <cluster>-app Secret (key uri) or an external
// connection string. TLS is required: the connection always uses
// sslmode=verify-full, with CAFile as the only trusted root when set (the
// CloudNativePG <cluster>-ca Secret, key ca.crt) and the system roots
// otherwise.
type Config struct {
	URL    string
	CAFile string
	// MaxConns bounds the pool (default 10).
	MaxConns int32
	// ConnectTimeout bounds one connection attempt (default 5s).
	ConnectTimeout time.Duration
}

// ConfigFromFiles reads the connection URL from urlFile (a mounted Secret
// key) and remembers caFile. The URL never appears in errors or logs.
func ConfigFromFiles(urlFile, caFile string) (Config, error) {
	raw, err := os.ReadFile(urlFile)
	if err != nil {
		return Config{}, errors.New("database URL file unreadable")
	}
	c := Config{URL: strings.TrimSpace(string(raw)), CAFile: caFile}
	if caFile != "" {
		if _, err := os.Stat(caFile); err != nil {
			return Config{}, errors.New("database CA file unreadable")
		}
	}
	return c, nil
}

// PoolConfig validates c and returns the pgx pool configuration. It refuses
// any sslmode other than verify-full and any root other than CAFile, so a
// connection string cannot turn TLS off or trust another CA.
func (c Config) PoolConfig() (*pgxpool.Config, error) {
	u, err := url.Parse(c.URL)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		// Never wrap err: url.Parse errors quote the input, password included.
		return nil, errors.New("database URL must be a postgres:// or postgresql:// URI with a host")
	}
	q := u.Query()
	if mode := q.Get("sslmode"); mode != "" && mode != "verify-full" {
		return nil, fmt.Errorf("database sslmode %q refused: TLS with sslmode=verify-full is required", mode)
	}
	q.Set("sslmode", "verify-full")
	if c.CAFile != "" {
		if root := q.Get("sslrootcert"); root != "" && root != c.CAFile {
			return nil, errors.New("database URL names another sslrootcert; set the CA file instead")
		}
		q.Set("sslrootcert", c.CAFile)
	} else if root := q.Get("sslrootcert"); root != "" && root != "system" {
		return nil, errors.New("database URL sslrootcert must be empty or system; set the CA file instead")
	}
	if q.Get("application_name") == "" {
		q.Set("application_name", "agent-project-controller")
	}
	u.RawQuery = q.Encode()
	pc, err := pgxpool.ParseConfig(u.String())
	if err != nil {
		return nil, errors.New("database URL could not be parsed")
	}
	// Belt and braces: every connection attempt must verify the server
	// certificate and host name.
	tlsConfigs := []*tls.Config{pc.ConnConfig.TLSConfig}
	for _, f := range pc.ConnConfig.Fallbacks {
		tlsConfigs = append(tlsConfigs, f.TLSConfig)
	}
	for _, t := range tlsConfigs {
		if t == nil || t.InsecureSkipVerify || t.ServerName == "" {
			return nil, errors.New("database connection would not verify the server certificate")
		}
		if t.MinVersion < tls.VersionTLS12 {
			t.MinVersion = tls.VersionTLS12
		}
	}
	pc.MaxConns = 10
	if c.MaxConns > 0 {
		pc.MaxConns = c.MaxConns
	}
	pc.MinConns = 0
	pc.ConnConfig.ConnectTimeout = 5 * time.Second
	if c.ConnectTimeout > 0 {
		pc.ConnConfig.ConnectTimeout = c.ConnectTimeout
	}
	pc.HealthCheckPeriod = 15 * time.Second
	pc.MaxConnIdleTime = 5 * time.Minute
	return pc, nil
}

package localadmin

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/ziad-hsn/cpra/internal/httpauth"
	"github.com/ziad-hsn/cpra/internal/installpath"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"gopkg.in/yaml.v3"
)

type ManagementInitOptions struct {
	Account, OperatorID, TokenOutput string
}

// ManagementFiles contains public locations only. The operator bearer is never
// returned, logged, placed in state, or made readable by a system service.
type ManagementFiles struct {
	Manifest, Runtime, Token, CA string
}

// InitManagement creates a fresh local management installation. It refuses an
// existing runtime, credential, or store; it never converts existing authority,
// opens Raft, installs browser trust, or starts/installs a service. Publication
// is exclusive and synchronous. A partial failure requires operator inspection.
func InitManagement(ctx context.Context, l installpath.Layout, options ManagementInitOptions) (ManagementFiles, error) {
	if ctx == nil || l.Scope != "user" && l.Scope != "system" || !filepath.IsAbs(l.ConfigDir) || !filepath.IsAbs(l.StateDir) {
		return ManagementFiles{}, errors.New("management initialization requires absolute configuration and state paths")
	}
	if err := ctx.Err(); err != nil {
		return ManagementFiles{}, err
	}
	if options.OperatorID == "" {
		options.OperatorID = "local/operator"
	}
	if options.TokenOutput == "" {
		if l.Scope == "system" {
			return ManagementFiles{}, errors.New("system management initialization requires --token-output outside service configuration and state")
		}
		options.TokenOutput = filepath.Join(l.ConfigDir, "operator.token")
	}
	if !filepath.IsAbs(options.TokenOutput) || insidePath(options.TokenOutput, l.StateDir) ||
		l.Scope == "system" && insidePath(options.TokenOutput, l.ConfigDir) {
		return ManagementFiles{}, errors.New("operator token output must be absolute and outside service-owned paths")
	}
	files := ManagementFiles{Manifest: filepath.Join(l.ConfigDir, "monitors.yaml"), Runtime: filepath.Join(l.ConfigDir, "runtime.yaml"), Token: options.TokenOutput, CA: filepath.Join(l.ConfigDir, "localhost-ca.pem")}
	policyPath := filepath.Join(l.ConfigDir, "management-policy.yaml")
	keyPath := filepath.Join(l.ConfigDir, "wrapping.key")
	certPath, certKeyPath := filepath.Join(l.ConfigDir, "localhost.pem"), filepath.Join(l.ConfigDir, "localhost.key")
	paths := []string{files.Manifest, files.Runtime, files.Token, files.CA, policyPath, keyPath, certPath, certKeyPath, filepath.Join(l.ConfigDir, "auth.token")}
	for _, path := range paths {
		if err := rejectSymlinkAncestors(path); err != nil {
			return ManagementFiles{}, err
		}
		if _, err := os.Lstat(path); err == nil {
			return ManagementFiles{}, errors.New("management initialization requires absent configuration and credential files")
		} else if !errors.Is(err, os.ErrNotExist) {
			return ManagementFiles{}, err
		}
	}
	if entries, err := os.ReadDir(l.StateDir); err == nil && len(entries) != 0 {
		return ManagementFiles{}, errors.New("existing state cannot be converted by management initialization; use stopped administration")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return ManagementFiles{}, err
	}
	token := make([]byte, 32)
	defer clear(token)
	if _, err := rand.Read(token); err != nil {
		return ManagementFiles{}, err
	}
	bearer := []byte(hex.EncodeToString(token))
	defer clear(bearer)
	verifier, err := httpauth.HashToken(string(bearer))
	if err != nil {
		return ManagementFiles{}, err
	}
	policy := httpauth.Config{Principals: []httpauth.Principal{{ID: options.OperatorID, Role: httpauth.Operator, TokenSHA256: verifier}}}
	if err := httpauth.ValidateConfig(policy); err != nil {
		return ManagementFiles{}, err
	}
	account, err := nativePrepare(l, options.Account)
	if err != nil {
		return ManagementFiles{}, err
	}
	group, sid, err := managementServiceReader(l, account)
	if err != nil {
		return ManagementFiles{}, err
	}
	for _, dir := range []string{l.ConfigDir, l.StateDir, l.LogDir} {
		if dir == "" {
			continue
		}
		if err := rejectSymlinkAncestors(dir); err != nil {
			return ManagementFiles{}, err
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			return ManagementFiles{}, err
		}
	}
	if err := nativeSecure(l, account); err != nil {
		return ManagementFiles{}, err
	}
	config := runtimeconfig.Default()
	config.Storage.Directory = l.StateDir
	config.Notifications.LogDirectory = filepath.Join(l.LogDir, "notifications")
	config.Management = runtimeconfig.Management{Enabled: true, PolicyFile: policyPath, PolicyReaderGroupID: group, PolicyReaderSID: sid,
		TLS:        &runtimeconfig.ManagementTLS{CertFile: certPath, KeyFile: certKeyPath, ReaderGroupID: group, ReaderSID: sid},
		Encryption: &runtimeconfig.ManagementEncryption{Local: &runtimeconfig.ManagementLocalKeys{ActiveKeyFile: keyPath, ReaderGroupID: group, ReaderSID: sid}}}
	if err := config.Validate(); err != nil {
		return ManagementFiles{}, err
	}
	ca, cert, private, err := localhostCertificate(time.Now().UTC())
	if err != nil {
		return ManagementFiles{}, err
	}
	defer clear(private)
	wrapping := make([]byte, 32)
	defer clear(wrapping)
	if _, err := rand.Read(wrapping); err != nil {
		return ManagementFiles{}, err
	}
	policyData, err := yaml.Marshal(policy)
	if err != nil {
		return ManagementFiles{}, err
	}
	runtimeData, err := yaml.Marshal(config)
	if err != nil {
		return ManagementFiles{}, err
	}
	manifest, err := templates.ReadFile("templates/monitors.yaml")
	if err != nil {
		return ManagementFiles{}, err
	}
	// Fixed order makes a partially initialized installation inspectable. The
	// runtime is published last; no initializer starts a reader of these files.
	for _, entry := range []struct {
		path string
		data []byte
	}{{policyPath, policyData}, {keyPath, wrapping}, {files.CA, ca}, {certPath, cert}, {certKeyPath, private}, {files.Manifest, manifest}} {
		if err := secureconfig.WriteNewProtectedFile(ctx, secureconfig.ProtectedFileOptions{Path: entry.path, DataDirectory: l.StateDir, ReaderGroupID: group, ReaderSID: sid, MaxBytes: 1 << 20}, entry.data); err != nil {
			return ManagementFiles{}, err
		}
	}
	if err := secureconfig.WriteNewProtectedFile(ctx, secureconfig.ProtectedFileOptions{Path: files.Token, DataDirectory: l.StateDir, MaxBytes: 128}, append(bearer, '\n')); err != nil {
		return ManagementFiles{}, err
	}
	if err := secureconfig.WriteNewProtectedFile(ctx, secureconfig.ProtectedFileOptions{Path: files.Runtime, DataDirectory: l.StateDir, ReaderGroupID: group, ReaderSID: sid, MaxBytes: 1 << 20}, runtimeData); err != nil {
		return ManagementFiles{}, err
	}
	return files, nil
}

func insidePath(path, parent string) bool {
	rel, err := filepath.Rel(parent, path)
	return err == nil && rel != ".." && !filepath.IsAbs(rel) && (rel == "." || len(rel) < 3 || rel[:3] != ".."+string(filepath.Separator))
}

// The local CA's private key is discarded after signing. Only the localhost
// leaf key is stored. Browser trust remains an explicit operator action.
func localhostCertificate(at time.Time) (ca, cert, key []byte, err error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	serial := func() (*big.Int, error) { return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128)) }
	caSerial, err := serial()
	if err != nil {
		return nil, nil, nil, err
	}
	leafSerial, err := serial()
	if err != nil {
		return nil, nil, nil, err
	}
	root := &x509.Certificate{SerialNumber: caSerial, Subject: pkix.Name{CommonName: "CPRa local browser trust"}, NotBefore: at.Add(-5 * time.Minute), NotAfter: at.AddDate(5, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, MaxPathLenZero: true}
	caDER, err := x509.CreateCertificate(rand.Reader, root, root, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, err
	}
	leaf := &x509.Certificate{SerialNumber: leafSerial, Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1), net.ParseIP("::1")}, NotBefore: at.Add(-5 * time.Minute), NotAfter: at.AddDate(1, 0, 0), BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, root, &leafKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		return nil, nil, nil, err
	}
	defer clear(keyDER)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
}

package platform

import (
	"strings"
	"testing"

	"github.com/mhenderik/sendora/internal/storage"
)

func ownS3(bucket, prefix string) map[string]string {
	return map[string]string{"storage_backend": "s3", "storage_s3_endpoint": "https://s3.example.com", "storage_s3_region": "eu-west-1",
		"storage_s3_bucket": bucket, "storage_s3_prefix": prefix, "storage_s3_auth": "static", "storage_s3_access_key": "AK", "storage_s3_secret_key": "SECRET"}
}

func TestTenantStorageResolveAndMask(t *testing.T) {
	p, err := Open(t.TempDir(), key32(t), true)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	a, err := p.CreateTenant("klanta", "A", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := p.CreateTenant("klantb", "B", 0, 0)
	sc, err := p.Storage(a.ID)
	if err != nil || sc.Mode != StorageModePlatform || sc.Prefix != "tenants/"+a.ID+"/" {
		t.Fatalf("default choice %+v %v", sc, err)
	}
	// Platform default local: the tenant's directory.
	if c := p.Resolve(a.ID, sc); c.Backend != storage.KindLocal {
		t.Fatalf("resolve with local default: %+v", c)
	}
	// Platform default remote: the tenant's fixed prefix inside it.
	next, err := MergeStorageSettings(p.StorageSettings(), ownS3("platform-bucket", "sendora/"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.PutStorageSettings(next, nil); err != nil {
		t.Fatal(err)
	}
	ca, cb := p.Resolve(a.ID, sc), p.Resolve(b.ID, nil)
	if ca.Prefix != "sendora/tenants/"+a.ID+"/" || cb.Prefix != "sendora/tenants/"+b.ID+"/" || ca.S3Bucket != "platform-bucket" || ca.Overlaps(cb) {
		t.Fatalf("tenant prefixes %q %q", ca.Prefix, cb.Prefix)
	}
	// An own bucket that overlaps the platform's tenants/ area is refused.
	if err := p.SetStorage(a.ID, StorageModeOwn, ownS3("platform-bucket", "sendora/tenants/x/"), false); err == nil {
		t.Fatal("own bucket inside the platform's tenants area accepted")
	}
	if err := p.SetStorage(a.ID, StorageModeOwn, ownS3("klanta-bucket", ""), true); err != nil {
		t.Fatal(err)
	}
	// Another tenant cannot point at the same bucket and prefix.
	if err := p.SetStorage(b.ID, StorageModeOwn, ownS3("klanta-bucket", "sub/"), false); err == nil || !strings.Contains(err.Error(), "another tenant") {
		t.Fatalf("overlapping own bucket accepted: %v", err)
	}
	sc, _ = p.Storage(a.ID)
	if !sc.TenantManaged || p.Resolve(a.ID, sc).S3Bucket != "klanta-bucket" {
		t.Fatalf("own choice %+v", sc)
	}
	v := sc.View()
	if v.Settings["storage_s3_secret_key"] != "" || !v.Set["storage_s3_secret_key"] || v.Settings["storage_s3_bucket"] != "klanta-bucket" {
		t.Fatalf("view %+v", v)
	}
	// A blank secret keeps the stored one; changing the endpoint needs it again.
	merged, err := MergeStorageSettings(sc.Config, map[string]string{"storage_s3_bucket": "klanta-bucket2", "storage_s3_secret_key": ""})
	if err != nil || merged["storage_s3_secret_key"] != "SECRET" {
		t.Fatalf("merge %v %v", merged, err)
	}
	if _, err := MergeStorageSettings(sc.Config, map[string]string{"storage_s3_endpoint": "https://evil.example.com"}); err == nil {
		t.Fatal("endpoint changed without retyping the secret")
	}
	if _, err := MergeStorageSettings(sc.Config, map[string]string{"backup_remote_enabled": "true"}); err == nil {
		t.Fatal("non-storage key accepted")
	}
	locs := p.StorageLocations(a.ID)
	if len(locs) != 2 {
		t.Fatalf("locations %+v", locs)
	}
	if err := p.SetStorage(a.ID, StorageModeOwn, map[string]string{"storage_backend": "local"}, false); err == nil {
		t.Fatal("local own bucket accepted")
	}
}

func TestPlatformMFASettingAndTemporaryPassword(t *testing.T) {
	p, err := Open(t.TempDir(), key32(t), true)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if !p.MFARequired() {
		t.Fatal("MFA must be required by default")
	}
	if _, err := p.PutSettings(map[string]string{"platform_mfa_required": "maybe"}, nil); err == nil {
		t.Fatal("invalid value accepted")
	}
	if _, err := p.PutSettings(map[string]string{"platform_mfa_required": "false"}, nil); err != nil || p.MFARequired() {
		t.Fatal("not switched off", err)
	}
	if _, err := p.PutSettings(map[string]string{"platform_storage_s3_bucket": "x"}, nil); err == nil {
		t.Fatal("storage setting through the generic settings")
	}
	a, err := p.CreateFirstAdmin("root", "", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SetTemporaryPassword(a.ID, "temp"); err != nil {
		t.Fatal(err)
	}
	a, _ = p.AdminByID(a.ID)
	if !a.MustChangePassword || a.PasswordHash != "temp" {
		t.Fatal("temporary password not marked")
	}
	p.SetPassword(a.ID, "new")
	if a, _ = p.AdminByID(a.ID); a.MustChangePassword {
		t.Fatal("mark not cleared by a new password")
	}
	if n := p.SettingIntRange("max_open_tenants", 50, 1, 10000); n != 50 {
		t.Fatal(n)
	}
	p.St.DB.Exec(`UPDATE settings SET value='99999999999999999999' WHERE key='max_open_tenants'`)
	if n := p.SettingIntRange("max_open_tenants", 50, 1, 10000); n != 50 {
		t.Fatal("unparsable value", n)
	}
	p.St.DB.Exec(`UPDATE settings SET value='-5' WHERE key='max_open_tenants'`)
	if n := p.SettingIntRange("max_open_tenants", 50, 1, 10000); n != 1 {
		t.Fatal("clamp", n)
	}
}

// Tenants may configure their own storage by default, also tenants stored
// before the switch existed; an explicit "off" from the console is kept.
func TestTenantManagedDefaultOn(t *testing.T) {
	dir, master := t.TempDir(), key32(t)
	p, err := Open(dir, master, true)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := p.CreateTenant("klanta", "A", 0, 0)
	b, _ := p.CreateTenant("klantb", "B", 0, 0)
	if sc, _ := p.Storage(a.ID); !sc.TenantManaged {
		t.Fatal("new tenant: own storage not allowed by default")
	}
	if err := p.SetStorage(b.ID, StorageModePlatform, nil, false); err != nil {
		t.Fatal(err)
	}
	// A row from an earlier build: off without an explicit choice.
	p.St.DB.Exec(`UPDATE tenant_storage SET tenant_managed=0, tenant_managed_explicit=0 WHERE tenant_id=?`, a.ID)
	p.Close()
	p, err = Open(dir, master, false)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if sc, _ := p.Storage(a.ID); !sc.TenantManaged {
		t.Fatal("existing tenant without a choice not switched on")
	}
	if sc, _ := p.Storage(b.ID); sc.TenantManaged {
		t.Fatal("explicit off was overridden")
	}
}

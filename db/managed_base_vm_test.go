package db

import (
	"strings"
	"testing"
)

// Migration 066 marks the VM projects whose base an agent set up, or would:
// those without a base or shared VM, and those whose base Shelley created
// for the project's setup. A base the user named, or a shared VM, is not.
func TestManagedBaseVMMigration(t *testing.T) {
	t.Parallel()
	database := setupDBMigratedThrough(t, 65)
	defer database.Close()
	ctx := t.Context()
	exec := func(query string) {
		t.Helper()
		if err := database.pool.Exec(ctx, query); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	want := map[string]ProjectVM{
		"Hub":   {SharedVM: "hub", Integration: "i"},
		"Setup": {ManagedBaseVM: true, Integration: "i"},
		"Done":  {Base: "done-base", ManagedBaseVM: true, Integration: "i"},
		"Mine":  {Base: "my-base", Integration: "i"},
	}
	for name := range want {
		exec(`INSERT INTO profiles (name, settings) SELECT '` + name + `', settings FROM profiles WHERE is_default`)
		exec(`INSERT INTO profile_projects (profile, cwd, position) SELECT '` + name + `', '/r', COUNT(*) FROM profile_projects`)
	}
	exec(`INSERT INTO profile_vms (profile, base_vm, shared_vm, integration) VALUES
		('Hub', '', 'hub', 'i'), ('Setup', '', '', 'i'), ('Done', 'done-base', '', 'i'), ('Mine', 'my-base', '', 'i')`)
	exec(`INSERT INTO profile_base_vms (profile, integration, vm_name, status, requested_at)
		VALUES ('Done', 'i', 'done-base', 'created', '2026-01-02T03:04:05Z')`)

	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	projects, err := database.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range projects {
		if vm, ok := want[p.Name]; ok && (p.VM == nil || *p.VM != vm) {
			t.Errorf("%s: VM %+v, want %+v", p.Name, p.VM, vm)
		}
		delete(want, p.Name)
	}
	if len(want) > 0 {
		t.Errorf("missing projects: %v", want)
	}
}

// A shared VM takes no base or managed_base_vm; conversations' own VMs need an
// integration, with or without a base. Without one, a shared VM is an ssh
// destination: no options, port, or path.
func TestProjectVMValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		vm    ProjectVM
		valid bool
	}{
		{ProjectVM{Base: "b", Integration: "i"}, true},
		{ProjectVM{Integration: "i"}, true},
		{ProjectVM{ManagedBaseVM: true, Integration: "i"}, true},
		{ProjectVM{Base: "b", ManagedBaseVM: true, Integration: "i"}, true},
		{ProjectVM{SharedVM: "s", Integration: "i"}, true},
		{ProjectVM{SharedVM: "s", Base: "b", Integration: "i"}, false},
		{ProjectVM{SharedVM: "s", ManagedBaseVM: true, Integration: "i"}, false},
		{ProjectVM{Base: "b"}, false},
		{ProjectVM{ManagedBaseVM: true}, false},
		{ProjectVM{}, false},
		{ProjectVM{SharedVM: "me@box"}, true},
		{ProjectVM{SharedVM: "me@box", Integration: "i"}, false},
		{ProjectVM{SharedVM: "-oProxyCommand=x"}, false},
		{ProjectVM{SharedVM: "me@box:22"}, false},
		{ProjectVM{SharedVM: strings.Repeat("a", 256)}, false},
	} {
		vm := tc.vm
		if err := validateProjectVM(&vm, "/r"); (err == nil) != tc.valid {
			t.Errorf("%+v: %v, want valid %v", tc.vm, err, tc.valid)
		}
	}
}

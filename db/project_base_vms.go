package db

import (
	"context"
	"time"
)

// ProjectBaseVM is a base VM Shelley created for a project: deleting the
// project deletes it. See 054-project-vms.sql.
type ProjectBaseVM struct {
	Project     string    `json:"project"`
	Name        string    `json:"vm_name"`
	Integration string    `json:"integration"`
	Status      string    `json:"status"` // ProjectBaseVMCreating or ProjectBaseVMCreated
	VMCreatedAt string    `json:"vm_created_at"`
	RequestedAt time.Time `json:"requested_at"`
}

// Base VM statuses.
const (
	ProjectBaseVMCreating = "creating"
	ProjectBaseVMCreated  = "created"
)

// RecordProjectBaseVM records (or updates) a base VM Shelley is creating or
// created for vm.Project.
func (db *DB) RecordProjectBaseVM(ctx context.Context, vm ProjectBaseVM) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec(`INSERT INTO profile_base_vms(integration,vm_name,profile,status,vm_created_at,requested_at)
			VALUES(?,?,?,?,?,?) ON CONFLICT(integration,vm_name) DO UPDATE SET
			profile=excluded.profile,status=excluded.status,vm_created_at=excluded.vm_created_at,requested_at=excluded.requested_at`,
			vm.Integration, vm.Name, vm.Project, vm.Status, vm.VMCreatedAt, vm.RequestedAt.UTC().Format(time.RFC3339Nano))
		return err
	})
}

// ForgetProjectBaseVM drops the record of a base VM that no longer exists.
func (db *DB) ForgetProjectBaseVM(ctx context.Context, integration, name string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec("DELETE FROM profile_base_vms WHERE integration=? AND vm_name=?", integration, name)
		return err
	})
}

// ListProjectBaseVMs returns the base VMs Shelley created for project, or for
// every project if it is "".
func (db *DB) ListProjectBaseVMs(ctx context.Context, project string) (vms []ProjectBaseVM, err error) {
	err = db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		vms, err = listProjectBaseVMs(rx, project)
		return err
	})
	return vms, err
}

func listProjectBaseVMs(rx *Rx, project string) ([]ProjectBaseVM, error) {
	out := []ProjectBaseVM{}
	rows, err := rx.Query(`SELECT profile,vm_name,integration,status,vm_created_at,requested_at FROM profile_base_vms
		WHERE ?='' OR profile=? ORDER BY requested_at,vm_name`, project, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var vm ProjectBaseVM
		var requested string
		if err := rows.Scan(&vm.Project, &vm.Name, &vm.Integration, &vm.Status, &vm.VMCreatedAt, &requested); err != nil {
			return nil, err
		}
		if vm.RequestedAt, err = time.Parse(time.RFC3339Nano, requested); err != nil {
			return nil, err
		}
		out = append(out, vm)
	}
	return out, rows.Err()
}

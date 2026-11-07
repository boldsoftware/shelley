package db

import (
	"context"
	"time"

	"shelley.exe.dev/db/generated"
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
	return db.QueriesTx(ctx, func(q *generated.Queries) error {
		return q.UpsertProjectBaseVM(ctx, generated.UpsertProjectBaseVMParams{
			Integration: vm.Integration, VmName: vm.Name, Profile: vm.Project, Status: vm.Status,
			VmCreatedAt: vm.VMCreatedAt, RequestedAt: formatTime(vm.RequestedAt),
		})
	})
}

// ForgetProjectBaseVM drops the record of a base VM that no longer exists.
func (db *DB) ForgetProjectBaseVM(ctx context.Context, integration, name string) error {
	return db.QueriesTx(ctx, func(q *generated.Queries) error {
		return q.DeleteProjectBaseVM(ctx, generated.DeleteProjectBaseVMParams{Integration: integration, VmName: name})
	})
}

// ListProjectBaseVMs returns the base VMs Shelley created for project, or for
// every project if it is "".
func (db *DB) ListProjectBaseVMs(ctx context.Context, project string) (vms []ProjectBaseVM, err error) {
	err = db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		vms, err = listProjectBaseVMs(ctx, generated.New(rx.Conn()), project)
		return err
	})
	return vms, err
}

func listProjectBaseVMs(ctx context.Context, q *generated.Queries, project string) ([]ProjectBaseVM, error) {
	rows, err := q.ListProjectBaseVMs(ctx, project)
	if err != nil {
		return nil, err
	}
	out := []ProjectBaseVM{}
	for _, row := range rows {
		vm := ProjectBaseVM{Project: row.Profile, Name: row.VmName, Integration: row.Integration, Status: row.Status, VMCreatedAt: row.VmCreatedAt}
		if vm.RequestedAt, err = parseTime(row.RequestedAt); err != nil {
			return nil, err
		}
		out = append(out, vm)
	}
	return out, nil
}

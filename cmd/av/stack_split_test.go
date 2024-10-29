package main

import (
	"github.com/aviator-co/av/internal/git"
	"github.com/aviator-co/av/internal/meta"
	"github.com/aviator-co/av/internal/sequencer/sequencerui"
	"reflect"
	"testing"
)

func Test_stackSplitViewModel_createState(t *testing.T) {
	type fields struct {
		repo             *git.Repo
		db               meta.DB
		stackSplitModel  *sequencerui.RestackModel
		quitWithConflict bool
		err              error
	}
	tests := []struct {
		name    string
		fields  fields
		want    *sequencerui.RestackState
		wantErr bool
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := &stackSplitViewModel{
				repo:             tt.fields.repo,
				db:               tt.fields.db,
				stackSplitModel:  tt.fields.stackSplitModel,
				quitWithConflict: tt.fields.quitWithConflict,
				err:              tt.fields.err,
			}
			got, err := vm.createState()
			if (err != nil) != tt.wantErr {
				t.Errorf("createState() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("createState() got = %v, want %v", got, tt.want)
			}
		})
	}
}

package store

import (
	"errors"
	"testing"
)

func TestKey_String_Table(t *testing.T) {
	tests := []struct {
		name string
		key  Key
		want string
	}{
		{
			name: "standard logical key formatting",
			key: Key{
				TenantID:  "tenant-1",
				SessionID: "sess-1",
				ContactID: "contact-1",
			},
			want: "tenant-1|sess-1|contact-1",
		},
		{
			name: "empty components key formatting",
			key: Key{
				TenantID:  "",
				SessionID: "",
				ContactID: "",
			},
			want: "||",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.key.String(); got != tt.want {
				t.Errorf("Key.String() = %q, quiero %q", got, tt.want)
			}
		})
	}
}

func TestStoreErrors_Table(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "ErrDefinitionNotFound non-nil and error target",
			err:  ErrDefinitionNotFound,
		},
		{
			name: "ErrTenantContentNotFound non-nil and error target",
			err:  ErrTenantContentNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err == nil {
				t.Errorf("error %s es nil", tt.name)
			}
			if !errors.Is(tt.err, tt.err) {
				t.Errorf("errors.Is(%v, %v) = false", tt.err, tt.err)
			}
		})
	}
}

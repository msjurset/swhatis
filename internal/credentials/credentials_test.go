package credentials

import "testing"

func TestCleanOpRef(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain op ref",
			in:   "op://Private/Gemini API Key/credential",
			want: "op://Private/Gemini API Key/credential",
		},
		{
			name: "double quoted with whitespace",
			in:   `  "op://Private/Gemini API Key/credential"  `,
			want: "op://Private/Gemini API Key/credential",
		},
		{
			name: "single quoted",
			in:   `'op://Vault/Item/field'`,
			want: "op://Vault/Item/field",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CleanOpRef(tt.in); got != tt.want {
				t.Errorf("CleanOpRef(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestIsOpRef(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{
			name: "valid op ref",
			in:   "op://Private/Gemini API Key/credential",
			want: true,
		},
		{
			name: "quoted op ref",
			in:   `"op://Private/Gemini API Key/credential"`,
			want: true,
		},
		{
			name: "raw api key",
			in:   "AIzaSyA1234567890",
			want: false,
		},
		{
			name: "empty string",
			in:   "",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsOpRef(tt.in); got != tt.want {
				t.Errorf("IsOpRef(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

package release_pruner

import (
	"reflect"
	"testing"
)

func Test_sortVersionDirs(t *testing.T) {
	tests := []struct {
		name string
		dirs []string
		want []string
	}{
		{
			name: "semver with v prefix",
			dirs: []string{"v1.0.0", "v2.0.0", "v1.1.0"},
			want: []string{"v2.0.0", "v1.1.0", "v1.0.0"},
		},
		{
			name: "semver without v prefix - fallback to lexicographic",
			dirs: []string{"1.0.0", "2.0.0", "1.1.0"},
			want: []string{"2.0.0", "1.1.0", "1.0.0"},
		},
		{
			name: "timestamp dirs - lexicographic",
			dirs: []string{"20240101_120000", "20240301_100000", "20240201_150000"},
			want: []string{"20240301_100000", "20240201_150000", "20240101_120000"},
		},
		{
			name: "mixed semver and non-semver",
			dirs: []string{"v1.0.0", "20240101_120000", "v2.0.0"},
			want: []string{"v2.0.0", "v1.0.0", "20240101_120000"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dirs := make([]string, len(tt.dirs))
			copy(dirs, tt.dirs)
			sortVersionDirs(dirs)
			if !reflect.DeepEqual(dirs, tt.want) {
				t.Errorf("sortVersionDirs() = %v, want %v", dirs, tt.want)
			}
		})
	}
}

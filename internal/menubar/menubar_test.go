package menubar

import (
	"reflect"
	"testing"
)

func TestOrphanPids(t *testing.T) {
	ps := `  32574     1 /Users/x/Library/Application Support/colima-dashboard/menubar/ColimaDashboardMenu
  53537 53440 /Users/x/Library/Application Support/colima-dashboard/menubar/ColimaDashboardMenu
  44830     1 /Users/x/Library/Application Support/colima-dashboard/menubar/ColimaDashboardMenu
    600     1 /usr/sbin/notColimaDashboardMenu
    700     1 /usr/sbin/syslogd
`
	if got, want := orphanPids(ps), []int{32574, 44830}; !reflect.DeepEqual(got, want) {
		t.Fatalf("orphanPids = %v, want %v", got, want)
	}
}

//go:build !windows

package ui

import (
	"os"
	"testing"
)

func TestLocaleStringIsUTF8(t *testing.T) {
	cases := []struct {
		locale string
		want   bool
	}{
		{"en_US.UTF-8", true},
		{"en_US.utf8", true},
		{"C.UTF-8", true},
		{"en_GB.UTF-8@euro", true},
		{"C", false},
		{"POSIX", false},
		{"ja_JP.eucJP", false},
		{"en_US.ISO-8859-1", false},
		{"", false},
	}
	for _, c := range cases {
		if got := localeStringIsUTF8(c.locale); got != c.want {
			t.Errorf("localeStringIsUTF8(%q) = %v, want %v", c.locale, got, c.want)
		}
	}
}

func TestLocaleSupportsUTF8_PrecedenceAndFallback(t *testing.T) {
	for _, name := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		orig, had := os.LookupEnv(name)
		t.Cleanup(func() {
			if had {
				os.Setenv(name, orig)
			} else {
				os.Unsetenv(name)
			}
		})
		os.Unsetenv(name)
	}

	if got := localeSupportsUTF8(); got != false {
		t.Errorf("with no locale vars set at all, expected false (POSIX/C locale), got %v", got)
	}

	os.Setenv("LANG", "en_US.UTF-8")
	if got := localeSupportsUTF8(); got != true {
		t.Errorf("with only LANG=en_US.UTF-8 set, expected true, got %v", got)
	}

	os.Setenv("LC_CTYPE", "C")
	if got := localeSupportsUTF8(); got != false {
		t.Errorf("LC_CTYPE=C must override LANG=en_US.UTF-8 (POSIX precedence), got %v", got)
	}

	os.Setenv("LC_ALL", "en_US.UTF-8")
	if got := localeSupportsUTF8(); got != true {
		t.Errorf("LC_ALL=en_US.UTF-8 must override both LC_CTYPE and LANG (POSIX precedence), got %v", got)
	}

	os.Setenv("LC_ALL", "C")
	if got := localeSupportsUTF8(); got != false {
		t.Errorf("LC_ALL=C must win even though LANG is still UTF-8, got %v", got)
	}
}

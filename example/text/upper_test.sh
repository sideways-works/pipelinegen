# Unit tests of upper.
. ./lib.sh

test_upper() {
	[ "$(upper hello)" = HELLO ] || fail "upper hello = $(upper hello), want HELLO"
}

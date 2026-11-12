# Unit tests of add. The runner sources this file in its package's directory
# and calls each test_ function; fail stops the test with a message.
. ./lib.sh

test_add_positive() {
	[ "$(add 2 3)" = 5 ] || fail "add 2 3 = $(add 2 3), want 5"
}

test_add_negative() {
	[ "$(add -4 4)" = 0 ] || fail "add -4 4 = $(add -4 4), want 0"
}

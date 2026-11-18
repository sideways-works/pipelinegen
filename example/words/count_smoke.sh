# WORDS is the address of the server the batch started.

test_count() {
	got=$(wget -qO- "$WORDS/count?s=a+b+c")
	[ "$got" = 3 ] || fail "/count?s=a+b+c = $got, want 3"
}

test_count_nothing() {
	got=$(wget -qO- "$WORDS/count?s=")
	[ "$got" = 0 ] || fail "/count?s= = $got, want 0"
}

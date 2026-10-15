package handlers

import "testing"

func TestGameDiscount(t *testing.T) {
	cases := map[int]int{0: 0, 4: 0, 5: 5, 14: 5, 15: 10, 29: 10, 30: 15, 49: 15, 50: 20, 300: 20}
	for destroyed, want := range cases {
		if got := gameDiscount(destroyed); got != want {
			t.Errorf("gameDiscount(%d) = %d, want %d", destroyed, got, want)
		}
	}
}

func TestDiscountCodeRe(t *testing.T) {
	for code, ok := range map[string]bool{
		"ANDRHO-10-A1B2C3": true,
		"ANDRHO-5-ZZZZZZ":  true,
		"ANDRHO-10-a1b2c3": false,
		"ANDRHO-10-A1B2":   false,
		"X-10-A1B2C3":      false,
	} {
		if discountCodeRe.MatchString(code) != ok {
			t.Errorf("discountCodeRe(%q) = %v, want %v", code, !ok, ok)
		}
	}
}

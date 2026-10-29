package tiktok

import "errors"

// User-facing failure conditions. Messages stay close to the reference tool so
// existing users recognise them; the help links point at this repository.
var (
	ErrCountryBlocked = errors.New("captcha required or country blocked - use a VPN, pass -room_id, or add cookies.json")
	ErrCountryAuto    = errors.New("automatic mode only works from a country TikTok does not block - use a VPN or add cookies.json")
	ErrCountryFollow  = errors.New("followers mode only works from a country TikTok does not block - use a VPN or add cookies.json")
	ErrPrivateRoom    = errors.New("live is private, login required - add your cookies to cookies.json")
	ErrPrivateFollow  = errors.New("this account is private, follow the creator to access their LIVE")
	ErrNotLive        = errors.New("the user is not hosting a live stream at the moment")
	ErrUserUnknown    = errors.New("username / room_id not found, or the user has never hosted a live")
	ErrRoomID         = errors.New("could not extract room_id")
	ErrLiveURL        = errors.New("unable to retrieve the live stream url, try again later")
	ErrInvalidURL     = errors.New("the provided url is not a valid TikTok live stream")
	ErrWAF            = errors.New("your IP is blocked by the TikTok WAF - change your IP address")
	ErrPrivateAccount = errors.New("account is private, login required - add your cookies to cookies.json")
)

// transient reports whether a failed attempt is worth retrying on the next poll.
// Login-gated errors are excluded: no amount of waiting unlocks a private room,
// and polling one forever buries the instruction to add cookies.
func transient(err error) bool {
	for _, target := range []error{
		ErrNotLive, ErrLiveURL, ErrUserUnknown, ErrWAF,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// Transient reports whether err is a recoverable "not live yet / try later"
// condition, as opposed to a fatal configuration or access error.
func Transient(err error) bool { return err != nil && transient(err) }

// fatal lists the conditions no retry can fix: they need the operator to add
// cookies, change region, or correct the arguments.
func fatal(err error) bool {
	for _, target := range []error{
		ErrPrivateRoom, ErrPrivateAccount, ErrPrivateFollow,
		ErrCountryBlocked, ErrCountryAuto, ErrCountryFollow,
		ErrInvalidURL, ErrRoomID,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// Fatal reports whether err needs the operator to act rather than waiting for
// the next poll.
func Fatal(err error) bool { return err != nil && fatal(err) }

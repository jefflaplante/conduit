package reflection

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// TriggerType.String
// ---------------------------------------------------------------------------

func TestTriggerTypeString(t *testing.T) {
	assert.Equal(t, "none", TriggerNone.String())
	assert.Equal(t, "farewell", TriggerFarewell.String())
	assert.Equal(t, "command", TriggerCommand.String())
}

// ---------------------------------------------------------------------------
// IsFarewell — positive cases
// ---------------------------------------------------------------------------

func TestIsFarewell_BasicPhrases(t *testing.T) {
	d := NewFarewellDetector()

	positives := []string{
		"goodbye",
		"Goodbye!",
		"GOODBYE",
		"bye",
		"bye!",
		"good night",
		"Good Night",
		"goodnight",
		"see you later",
		"See ya!",
		"talk later",
		"that's all",
		"that's it",
		"all done",
		"we're done",
		"thanks that's it",
		"thanks I'm done",
		"thank you that's all",
		"signing off",
		"logging off",
		"end session",
	}

	for _, msg := range positives {
		assert.True(t, d.IsFarewell(msg), "expected farewell for %q", msg)
	}
}

func TestIsFarewell_WhitespacePadding(t *testing.T) {
	d := NewFarewellDetector()

	assert.True(t, d.IsFarewell(" goodbye "))
	assert.True(t, d.IsFarewell("  bye  "))
	assert.True(t, d.IsFarewell("\tgood night\n"))
}

func TestIsFarewell_TrailingPunctuation(t *testing.T) {
	d := NewFarewellDetector()

	assert.True(t, d.IsFarewell("Goodbye!"))
	assert.True(t, d.IsFarewell("Bye."))
	assert.True(t, d.IsFarewell("see you later..."))
	assert.True(t, d.IsFarewell("all done!!"))
	assert.True(t, d.IsFarewell("that's it!"))
}

func TestIsFarewell_StartsWithFarewell(t *testing.T) {
	d := NewFarewellDetector()

	// Message starts with a farewell phrase followed by additional text.
	assert.True(t, d.IsFarewell("Goodbye, thanks for the help"))
	assert.True(t, d.IsFarewell("bye for now"))
	assert.True(t, d.IsFarewell("good night everyone"))
	assert.True(t, d.IsFarewell("see you later alligator"))
	assert.True(t, d.IsFarewell("thanks that's it for today"))
	assert.True(t, d.IsFarewell("all done here"))
	assert.True(t, d.IsFarewell("signing off for the night"))
}

// ---------------------------------------------------------------------------
// IsFarewell — negative cases
// ---------------------------------------------------------------------------

func TestIsFarewell_NegativeMidSentence(t *testing.T) {
	d := NewFarewellDetector()

	negatives := []string{
		"I need to say goodbye to the old API",
		"The goodbye message needs updating",
		"Can you fix the end session handler?",
		"please reset the counter",
		"Let's end this discussion about APIs",
		"say bye to the old code",
		"the goodnight routine is broken",
	}

	for _, msg := range negatives {
		assert.False(t, d.IsFarewell(msg), "should NOT detect farewell in %q", msg)
	}
}

func TestIsFarewell_NegativeMisc(t *testing.T) {
	d := NewFarewellDetector()

	assert.False(t, d.IsFarewell("hello"))
	assert.False(t, d.IsFarewell(""))
	assert.False(t, d.IsFarewell("   "))
	assert.False(t, d.IsFarewell("how are you?"))
	assert.False(t, d.IsFarewell("tell me about sessions"))
	assert.False(t, d.IsFarewell("can you help me?"))
}

func TestIsFarewell_NegativeEmbeddedWord(t *testing.T) {
	d := NewFarewellDetector()

	// "bye" embedded in "bypass", "byebye" as a non-standard form,
	// or "goodbye" as part of a compound word.
	assert.False(t, d.IsFarewell("bypass the filter"))
	assert.False(t, d.IsFarewell("goodbyeworld"))
}

// ---------------------------------------------------------------------------
// IsSessionEndCommand — positive and negative
// ---------------------------------------------------------------------------

func TestIsSessionEndCommand_Positive(t *testing.T) {
	d := NewFarewellDetector()

	assert.True(t, d.IsSessionEndCommand("/goodbye"))
	assert.True(t, d.IsSessionEndCommand("/end"))
	assert.True(t, d.IsSessionEndCommand("/reset"))

	// Case insensitive
	assert.True(t, d.IsSessionEndCommand("/GOODBYE"))
	assert.True(t, d.IsSessionEndCommand("/End"))
	assert.True(t, d.IsSessionEndCommand("/RESET"))

	// Whitespace trimming
	assert.True(t, d.IsSessionEndCommand(" /goodbye "))
	assert.True(t, d.IsSessionEndCommand("  /end\t"))
}

func TestIsSessionEndCommand_Negative(t *testing.T) {
	d := NewFarewellDetector()

	assert.False(t, d.IsSessionEndCommand("goodbye"))     // missing slash
	assert.False(t, d.IsSessionEndCommand(""))
	assert.False(t, d.IsSessionEndCommand("/help"))
	assert.False(t, d.IsSessionEndCommand("/status"))
	assert.False(t, d.IsSessionEndCommand("please /end")) // not the whole message
	assert.False(t, d.IsSessionEndCommand("/ender"))       // not a valid command
}

// ---------------------------------------------------------------------------
// ShouldTriggerReflection — combined
// ---------------------------------------------------------------------------

func TestShouldTriggerReflection_Command(t *testing.T) {
	d := NewFarewellDetector()

	ok, tt := d.ShouldTriggerReflection("/goodbye")
	assert.True(t, ok)
	assert.Equal(t, TriggerCommand, tt)

	ok, tt = d.ShouldTriggerReflection("/end")
	assert.True(t, ok)
	assert.Equal(t, TriggerCommand, tt)

	ok, tt = d.ShouldTriggerReflection("/reset")
	assert.True(t, ok)
	assert.Equal(t, TriggerCommand, tt)
}

func TestShouldTriggerReflection_Farewell(t *testing.T) {
	d := NewFarewellDetector()

	ok, tt := d.ShouldTriggerReflection("goodbye")
	assert.True(t, ok)
	assert.Equal(t, TriggerFarewell, tt)

	ok, tt = d.ShouldTriggerReflection("Bye!")
	assert.True(t, ok)
	assert.Equal(t, TriggerFarewell, tt)

	ok, tt = d.ShouldTriggerReflection("see you later")
	assert.True(t, ok)
	assert.Equal(t, TriggerFarewell, tt)
}

func TestShouldTriggerReflection_None(t *testing.T) {
	d := NewFarewellDetector()

	ok, tt := d.ShouldTriggerReflection("hello")
	assert.False(t, ok)
	assert.Equal(t, TriggerNone, tt)

	ok, tt = d.ShouldTriggerReflection("")
	assert.False(t, ok)
	assert.Equal(t, TriggerNone, tt)

	ok, tt = d.ShouldTriggerReflection("I need to say goodbye to the old API")
	assert.False(t, ok)
	assert.Equal(t, TriggerNone, tt)
}

func TestShouldTriggerReflection_CommandTakesPrecedence(t *testing.T) {
	d := NewFarewellDetector()

	// "/goodbye" is both a command and contains a farewell word.
	// Command should take precedence (higher confidence).
	ok, tt := d.ShouldTriggerReflection("/goodbye")
	assert.True(t, ok)
	assert.Equal(t, TriggerCommand, tt)
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

func TestIsFarewell_CurlyApostrophe(t *testing.T) {
	d := NewFarewellDetector()

	// People may type straight apostrophes. The phrases are stored with
	// straight apostrophes, so straight quotes must match.
	assert.True(t, d.IsFarewell("that's all"))
	assert.True(t, d.IsFarewell("we're done"))
}

func TestIsFarewell_PhraseFollowedByComma(t *testing.T) {
	d := NewFarewellDetector()

	assert.True(t, d.IsFarewell("goodbye, see you tomorrow"))
	assert.True(t, d.IsFarewell("bye, thanks!"))
}

func TestIsFarewell_ExclamationOnly(t *testing.T) {
	d := NewFarewellDetector()

	// A message that is just punctuation after trimming should not match.
	assert.False(t, d.IsFarewell("!"))
	assert.False(t, d.IsFarewell("..."))
}

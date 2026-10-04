package api

// A small music catalogue. Energy and valence are 0..1 audio features; plays is the popularity.
type song struct {
	id, title, artist string
	genres            []string
	energy, valence   float64
	plays             float64
}

var catalogue = []song{
	{"back_in_black", "Back in Black", "AC/DC", []string{"rock", "hard rock"}, 0.92, 0.65, 1500},
	{"thunderstruck", "Thunderstruck", "AC/DC", []string{"rock", "hard rock"}, 0.95, 0.45, 1300},
	{"enter_sandman", "Enter Sandman", "Metallica", []string{"metal", "hard rock"}, 0.90, 0.30, 1500},
	{"paranoid", "Paranoid", "Black Sabbath", []string{"rock", "metal"}, 0.90, 0.35, 700},
	{"crazy_train", "Crazy Train", "Ozzy Osbourne", []string{"rock", "metal", "hard rock"}, 0.85, 0.60, 600},
	{"whole_lotta_love", "Whole Lotta Love", "Led Zeppelin", []string{"rock", "hard rock"}, 0.85, 0.45, 900},
	{"teen_spirit", "Smells Like Teen Spirit", "Nirvana", []string{"rock", "grunge", "alternative"}, 0.91, 0.35, 1600},
	{"mr_brightside", "Mr. Brightside", "The Killers", []string{"rock", "indie"}, 0.90, 0.50, 1900},
	{"bohemian", "Bohemian Rhapsody", "Queen", []string{"rock", "classic rock"}, 0.60, 0.40, 2100},
	{"hotel_california", "Hotel California", "Eagles", []string{"rock", "classic rock", "folk rock"}, 0.45, 0.35, 1500},
	{"wonderwall", "Wonderwall", "Oasis", []string{"rock", "britpop"}, 0.55, 0.45, 1700},
	{"blinding_lights", "Blinding Lights", "The Weeknd", []string{"pop", "synth-pop"}, 0.80, 0.33, 4000},
	{"levitating", "Levitating", "Dua Lipa", []string{"pop", "dance"}, 0.83, 0.92, 2500},
	{"shake_it_off", "Shake It Off", "Taylor Swift", []string{"pop"}, 0.80, 0.94, 2200},
	{"strobe", "Strobe", "deadmau5", []string{"electronic", "progressive house"}, 0.55, 0.30, 300},
	{"clair_de_lune", "Clair de Lune", "Debussy", []string{"classical"}, 0.10, 0.30, 400},
}

// Other people's playlists: which songs end up together (the co_listed signal).
var playlists = [][]string{
	{"back_in_black", "thunderstruck", "enter_sandman", "paranoid"},
	{"back_in_black", "thunderstruck", "whole_lotta_love", "crazy_train"},
	{"thunderstruck", "enter_sandman", "crazy_train", "paranoid"},
	{"back_in_black", "whole_lotta_love", "bohemian", "hotel_california"},
	{"teen_spirit", "mr_brightside", "wonderwall"},
	{"blinding_lights", "levitating", "shake_it_off"},
	{"strobe", "blinding_lights"},
}

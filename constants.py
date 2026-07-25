"""
Constants module for FLAC Analyzer
===================================
"""

CHORDS_DIC = {
    "notes": ["C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"],
    "chords_dic": {
        "C": ["C", "E", "G"],
        "C#": ["C#", "F", "G#"],
        "D": ["D", "F#", "A"],
        "D#": ["D#", "G", "A#"],
        "E": ["E", "G#", "B"],
        "F": ["F", "A", "C"],
        "F#": ["F#", "A#", "C#"],
        "G": ["G", "B", "D"],
        "G#": ["G#", "C", "D#"],
        "A": ["A", "C#", "E"],
        "A#": ["A#", "D", "F"],
        "B": ["B", "D#", "F#"],
        "Cm": ["C", "D#", "G"],
        "C#m": ["C#", "E", "G#"],
        "Dm": ["D", "F", "A"],
        "D#m": ["D#", "F#", "A#"],
        "Em": ["E", "G", "B"],
        "Fm": ["F", "G#", "C"],
        "F#m": ["F#", "A", "C#"],
        "Gm": ["G", "A#", "D"],
        "G#m": ["G#", "B", "D#"],
        "Am": ["A", "C", "E"],
        "A#m": ["A#", "C#", "F"],
        "Bm": ["B", "D", "F#"],
    },
}

NOTES: list[str] = list(CHORDS_DIC["notes"])  # type: ignore[assignment]

DEFAULT_CLASS_MAP = {
    "danceability": ["danceable", "not_danceable"],
    "fs_loop_ds": [
        "bass",
        "drums",
        "guitar",
        "mallets",
        "other",
        "percussion",
        "piano",
        "strings",
        "synth",
        "vocal",
    ],
    "gender": ["male", "female"],
    "genre_dortmund": [
        "alternative",
        "blues",
        "electronic",
        "folkcountry",
        "funksoulrnb",
        "jazz",
        "pop",
        "raphiphop",
        "rock",
    ],
    "genre_electronic": ["electronic", "non_electronic"],
    "genre_rosamerica": [
        "classical",
        "dance",
        "hiphop",
        "jazz",
        "pop",
        "rhythm_and_blues",
        "rock",
        "speech",
    ],
    "genre_tzanetakis": [
        "blues",
        "classical",
        "country",
        "disco",
        "hiphop",
        "jazz",
        "metal",
        "pop",
        "reggae",
        "rock",
    ],
    "mood_acoustic": ["acoustic", "non_acoustic"],
    "mood_aggressive": ["aggressive", "non_aggressive"],
    "mood_electronic": ["electronic", "non_electronic"],
    "mood_happy": ["happy", "non_happy"],
    "mood_party": ["party", "non_party"],
    "mood_relaxed": ["relaxed", "non_relaxed"],
    "mood_sad": ["sad", "non_sad"],
    "approachability_3c": [
        "not approachable",
        "moderately approachable",
        "approachable",
    ],
    "engagement_3c": ["not engaging", "moderately engaging", "engaging"],
    "moods_mirex": ["cluster1", "cluster2", "cluster3", "cluster4", "cluster5"],
    "voice_instrumental": ["instrumental", "voice"],
}

CLASS_ALIAS = {
    "cla": "classical",
    "dan": "dance",
    "hip": "hiphop",
    "jaz": "jazz",
    "rhy": "rhythm_and_blues",
    "roc": "rock",
    "spe": "speech",
    "blu": "blues",
    "cou": "country",
    "dis": "disco",
    "met": "metal",
    "reg": "reggae",
    "folkcountr": "folkcountry",
}

# Krumhansl-Schmuckler Key Profiles
KEY_PROFILES = {
    "major": [6.35, 2.23, 3.48, 2.33, 4.38, 4.09, 2.52, 5.19, 2.39, 3.66, 2.29, 2.88],
    "minor": [6.33, 2.68, 3.52, 5.38, 2.60, 3.53, 2.54, 4.75, 3.98, 2.69, 3.34, 3.17],
}

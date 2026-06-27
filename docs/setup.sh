#!/bin/bash
mkdir models
cd models/

wget -q https://essentia.upf.edu/models/classification-heads/approachability/approachability_3c-discogs-effnet-1.json
wget -q https://essentia.upf.edu/models/classification-heads/approachability/approachability_3c-discogs-effnet-1.onnx
wget -q https://essentia.upf.edu/models/classification-heads/danceability/danceability-discogs-effnet-1.json
wget -q https://essentia.upf.edu/models/classification-heads/danceability/danceability-discogs-effnet-1.onnx
wget -q https://essentia.upf.edu/models/classification-heads/engagement/engagement_3c-discogs-effnet-1.json
wget -q https://essentia.upf.edu/models/classification-heads/engagement/engagement_3c-discogs-effnet-1.onnx
wget -q https://essentia.upf.edu/models/classification-heads/gender/gender-discogs-effnet-1.json
wget -q https://essentia.upf.edu/models/classification-heads/gender/gender-discogs-effnet-1.onnx
wget -q https://essentia.upf.edu/models/classification-heads/genre_dortmund/genre_dortmund-discogs-effnet-1.json
wget -q https://essentia.upf.edu/models/classification-heads/genre_dortmund/genre_dortmund-discogs-effnet-1.onnx
wget -q https://essentia.upf.edu/models/classification-heads/genre_electronic/genre_electronic-discogs-effnet-1.json
wget -q https://essentia.upf.edu/models/classification-heads/genre_electronic/genre_electronic-discogs-effnet-1.onnx
wget -q https://essentia.upf.edu/models/classification-heads/genre_rosamerica/genre_rosamerica-discogs-effnet-1.json
wget -q https://essentia.upf.edu/models/classification-heads/genre_rosamerica/genre_rosamerica-discogs-effnet-1.onnx
wget -q https://essentia.upf.edu/models/classification-heads/genre_tzanetakis/genre_tzanetakis-discogs-effnet-1.json
wget -q https://essentia.upf.edu/models/classification-heads/genre_tzanetakis/genre_tzanetakis-discogs-effnet-1.onnx
wget -q https://essentia.upf.edu/models/classification-heads/mood_acoustic/mood_acoustic-discogs-effnet-1.json
wget -q https://essentia.upf.edu/models/classification-heads/mood_acoustic/mood_acoustic-discogs-effnet-1.onnx
wget -q https://essentia.upf.edu/models/classification-heads/mood_aggressive/mood_aggressive-discogs-effnet-1.json
wget -q https://essentia.upf.edu/models/classification-heads/mood_aggressive/mood_aggressive-discogs-effnet-1.onnx
wget -q https://essentia.upf.edu/models/classification-heads/mood_electronic/mood_electronic-discogs-effnet-1.json
wget -q https://essentia.upf.edu/models/classification-heads/mood_electronic/mood_electronic-discogs-effnet-1.onnx
wget -q https://essentia.upf.edu/models/classification-heads/mood_happy/mood_happy-discogs-effnet-1.json
wget -q https://essentia.upf.edu/models/classification-heads/mood_happy/mood_happy-discogs-effnet-1.onnx
wget -q https://essentia.upf.edu/models/classification-heads/mood_party/mood_party-discogs-effnet-1.json
wget -q https://essentia.upf.edu/models/classification-heads/mood_party/mood_party-discogs-effnet-1.onnx
wget -q https://essentia.upf.edu/models/classification-heads/mood_relaxed/mood_relaxed-discogs-effnet-1.json
wget -q https://essentia.upf.edu/models/classification-heads/mood_relaxed/mood_relaxed-discogs-effnet-1.onnx
wget -q https://essentia.upf.edu/models/classification-heads/mood_sad/mood_sad-discogs-effnet-1.json
wget -q https://essentia.upf.edu/models/classification-heads/mood_sad/mood_sad-discogs-effnet-1.onnx
wget -q https://essentia.upf.edu/models/classification-heads/voice_instrumental/voice_instrumental-discogs-effnet-1.json
wget -q https://essentia.upf.edu/models/classification-heads/voice_instrumental/voice_instrumental-discogs-effnet-1.onnx
wget -q https://essentia.upf.edu/models/feature-extractors/discogs-effnet/discogs-effnet-bs64-1.json
wget -q https://essentia.upf.edu/models/feature-extractors/discogs-effnet/discogs-effnet-bs64-1.pb
#wget -q https://essentia.upf.edu/models/music-style-classification/discogs-effnet/discogs-effnet-bs64-1.pb
wget -q https://essentia.upf.edu/models/feature-extractors/discogs-effnet/discogs_artist_embeddings-effnet-bs64-1.json
wget -q https://essentia.upf.edu/models/feature-extractors/discogs-effnet/discogs_artist_embeddings-effnet-bs64-1.onnx
wget -q https://essentia.upf.edu/models/feature-extractors/discogs-effnet/discogs_label_embeddings-effnet-bs64-1.json
wget -q https://essentia.upf.edu/models/feature-extractors/discogs-effnet/discogs_label_embeddings-effnet-bs64-1.onnx
wget -q https://essentia.upf.edu/models/feature-extractors/discogs-effnet/discogs_multi_embeddings-effnet-bs64-1.json
wget -q https://essentia.upf.edu/models/feature-extractors/discogs-effnet/discogs_multi_embeddings-effnet-bs64-1.onnx
wget -q https://essentia.upf.edu/models/feature-extractors/discogs-effnet/discogs_release_embeddings-effnet-bs64-1.json
wget -q https://essentia.upf.edu/models/feature-extractors/discogs-effnet/discogs_release_embeddings-effnet-bs64-1.onnx
wget -q https://essentia.upf.edu/models/feature-extractors/discogs-effnet/discogs_track_embeddings-effnet-bs64-1.json
wget -q https://essentia.upf.edu/models/feature-extractors/discogs-effnet/discogs_track_embeddings-effnet-bs64-1.onnx
wget -q https://essentia.upf.edu/models/feature-extractors/maest/discogs-maest-30s-pw-519l-2.json
wget -q https://essentia.upf.edu/models/feature-extractors/maest/discogs-maest-30s-pw-519l-2.onnx

cd ..
sudo apt -y install python3.13
python3.13 -m venv .venv
. .\.venv\bin\activate
python -m pip install --upgrade pip
pip install -r requirements.txt


pip install flatbuffers packaging protobuf sympy coloredlogs
pip install --pre --index-url https://aiinfra.pkgs.visualstudio.com/PublicPackages/_packaging/ORT-Nightly/pypi/simple/ onnxruntime --no-deps
pip install --pre --index-url https://aiinfra.pkgs.visualstudio.com/PublicPackages/_packaging/ort-cuda-13-nightly/pypi/simple/ onnxruntime-gpu --no-deps
pip uninstall flatbuffers protobuf sympy coloredlogs

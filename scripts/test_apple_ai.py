#!/usr/bin/env python3
"""Native smoke test using installed Apple resources; never installs language/model assets."""
import json
import pathlib
import subprocess
import tempfile
import wave

ROOT = pathlib.Path(__file__).resolve().parents[1]
HELPER = ROOT / 'build/apple-ai/CineInsightAppleAI.app/Contents/MacOS/cineinsight-apple-ai'

def request(**value):
    result = subprocess.run([str(HELPER)], input=json.dumps(value), text=True, capture_output=True, timeout=90, check=True)
    return json.loads(result.stdout)

status = request(operation='status')
assert status.get('supported'), status
installed = {entry['code'].split('_')[0] for entry in status['languages'] if entry['installed']}
assert {'zh', 'en'} <= installed, 'Smoke test requires preinstalled Chinese and English speech resources'
with tempfile.TemporaryDirectory(prefix='cineinsight-apple-smoke-') as directory:
    for name, voice, text, source, expected in [
        ('english', 'Samantha', 'We are watching a movie tonight. Please wait for me here.', 'en_US', 'movie'),
        ('chinese', 'Tingting (中文（中国大陆）)', '今天我们看一部电影。请在这里等我。', 'zh_CN', '电影'),
    ]:
        path = pathlib.Path(directory) / (name + '.aiff')
        subprocess.run(['say', '-v', voice, '-o', str(path), text], check=True, timeout=20)
        result = request(operation='transcribe', path=str(path), source=source)
        assert not result.get('error'), result
        words = result['segments']
        assert words and all(word['end'] > word['start'] >= 0 for word in words), result
        assert expected in ''.join(word['text'] for word in words), result
        detected = request(operation='detect', path=str(path))
        assert detected.get('language', '').split('_')[0] == source.split('_')[0], detected
        print(name + ': transcription, timing and editable language candidate passed')
    silence = pathlib.Path(directory) / 'silence.wav'
    with wave.open(str(silence), 'wb') as output:
        output.setnchannels(1); output.setsampwidth(2); output.setframerate(16000)
        output.writeframes(b'\0\0' * 16000 * 2)
    assert request(operation='detect', path=str(silence)).get('error') == 'apple_language_uncertain'
    print('silence: no guessed language')

result = request(operation='translate', source='en', target='zh', texts=['We are watching a movie tonight.', 'Please wait for me here.'])
assert not result.get('error') and len(result['translations']) == 2 and all(result['translations']), result
assert request(operation='translate', source='en', target='zh', texts=[])['translations'] == []
assert request(operation='not-a-command')['error'] == 'apple_invalid_request'
print('translation batch, empty batch and invalid request passed')

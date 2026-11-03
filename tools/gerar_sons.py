"""Sintetiza os efeitos sonoros que acompanham o Deixa (campainhas e freios).

Os sons são gerados por síntese (sem gravações de terceiros), então podem ser
distribuídos livremente junto com o aplicativo. Uso:

    python3 tools/gerar_sons.py sons/

Requer apenas numpy.
"""
import sys, wave, pathlib
import numpy as np

RATE = 44100
rng = np.random.default_rng(7)


def t_axis(seconds):
    return np.arange(int(RATE * seconds)) / RATE


def smooth_noise(n, rate_hz, seed=None):
    """Ruído suave (banda limitada a ~rate_hz) por interpolação de pontos aleatórios."""
    r = np.random.default_rng(seed) if seed is not None else rng
    pts = max(4, int(n / RATE * rate_hz) + 2)
    knots = r.standard_normal(pts)
    return np.interp(np.linspace(0, pts - 1, n), np.arange(pts), knots)


def band(x, lo, hi):
    """Filtro passa-faixa por FFT (adequado para geração offline)."""
    spec = np.fft.rfft(x)
    f = np.fft.rfftfreq(len(x), 1 / RATE)
    # bordas suaves para evitar ringing
    w = np.clip((f - lo * 0.8) / (lo * 0.2 + 1e-9), 0, 1) if lo > 0 else np.ones_like(f)
    w *= np.clip((hi * 1.25 - f) / (hi * 0.25), 0, 1)
    return np.fft.irfft(spec * w, len(x))


def reverb(x, seconds=0.6, mix=0.18, seed=3):
    """Reverb de sala simples: convolução com ruído de decaimento exponencial."""
    n = int(RATE * seconds)
    r = np.random.default_rng(seed)
    ir = r.standard_normal(n) * np.exp(-np.arange(n) / RATE * (6.9 / seconds))
    ir = band(ir, 200, 7000)
    ir /= np.sqrt(np.sum(ir ** 2))
    size = len(x) + n
    wet = np.fft.irfft(np.fft.rfft(x, size) * np.fft.rfft(ir, size), size)
    dry = np.concatenate([x, np.zeros(n)])
    return dry * (1 - mix) + wet * mix * 3


def finish(x, peak=0.89, fade=0.25):
    x = x - np.mean(x)
    # corta o silêncio do fim (abaixo de -60 dB) e faz um fade-out suave
    level = np.abs(x) / (np.max(np.abs(x)) + 1e-9)
    last = np.nonzero(level > 0.001)[0]
    x = x[:last[-1] + 1] if len(last) else x
    f = min(int(RATE * fade), len(x) // 4)
    x[-f:] *= np.linspace(1, 0, f)
    x[:64] *= np.linspace(0, 1, 64)
    return x / (np.max(np.abs(x)) + 1e-9) * peak


def save(path, x):
    data = (np.clip(x, -1, 1) * 32767).astype('<i2')
    with wave.open(str(path), 'wb') as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(RATE)
        w.writeframes(data.tobytes())


# ---------------------------------------------------------------- campainhas

def chime_note(freq, seconds, strike=1.0):
    """Nota de carrilhão (barra de metal percutida, como em campainhas ding-dong)."""
    t = t_axis(seconds)
    # razões de parciais de uma barra livre + leve desafinação
    partials = [(1.0, 1.0, 1.35), (2.0, 0.18, 2.4), (2.756, 0.35, 3.8),
                (5.404, 0.12, 7.0), (8.933, 0.05, 12.0)]
    x = np.zeros_like(t)
    for ratio, amp, decay in partials:
        x += amp * np.sin(2 * np.pi * freq * ratio * t + rng.uniform(0, 6.28)) * np.exp(-t * decay)
    # batida do martelo (clique curto e abafado)
    click = band(rng.standard_normal(len(t)), 800, 5000) * np.exp(-t * 180) * 0.6
    att = np.clip(t / 0.002, 0, 1)
    return (x * att + click) * strike


def campainha_ding_dong():
    total = 4.6
    x = np.zeros(int(RATE * total))
    ding = chime_note(659.25, 3.6)           # Mi5
    dong = chime_note(523.25, total - 0.62)  # Dó5
    x[:len(ding)] += ding
    s = int(RATE * 0.62)
    x[s:s + len(dong)] += dong * 1.05
    return finish(reverb(x, 0.9, 0.22))


def campainha_eletrica():
    """Campainha elétrica de porta/escola: martelo batendo ~22x por segundo."""
    ring, tail = 2.4, 1.2
    t = t_axis(ring + tail)
    n = len(t)
    base = 1850.0
    partials = [(1.0, 1.0), (1.51, 0.55), (2.03, 0.4), (2.74, 0.3), (3.62, 0.18), (4.9, 0.1)]
    hit_rate = 22.0
    x = np.zeros(n)
    hits = np.arange(0, ring, 1 / hit_rate)
    hits += rng.normal(0, 0.0015, len(hits))
    impulse = np.zeros(n)
    for h in hits:
        i = int(max(0, h) * RATE)
        impulse[i] += rng.uniform(0.8, 1.0)
    # cada parcial é um ressonador: impulsos convoluídos com seno decrescente
    k = t_axis(1.2)
    for ratio, amp in partials:
        decay = 3.0 + ratio * 1.6
        resp = np.sin(2 * np.pi * base * ratio * k) * np.exp(-k * decay) * amp
        size = n + len(resp)
        y = np.fft.irfft(np.fft.rfft(impulse, size) * np.fft.rfft(resp, size), size)[:n]
        x += y
    # zumbido do mecanismo
    buzz = band(np.sign(np.sin(2 * np.pi * hit_rate * t)) * (t < ring), 60, 900) * 0.12
    x = np.tanh((x / np.max(np.abs(x))) * 1.6) + buzz
    return finish(reverb(x, 0.7, 0.15))


# ---------------------------------------------------------------- freios

def freada(seconds=2.4, f0=1150.0, drop=0.18, seed=11, stop_bump=True):
    """Cantada de pneu: tom áspero com harmônicos, modulação irregular e chiado."""
    r = np.random.default_rng(seed)
    t = t_axis(seconds)
    n = len(t)
    prog = t / seconds
    # altura cai um pouco com a desaceleração + oscilação irregular
    freq = f0 * (1 + 0.04 * smooth_noise(n, 6, seed) + 0.015 * smooth_noise(n, 35, seed + 1))
    freq *= (1 - drop * prog ** 1.6)
    phase = 2 * np.pi * np.cumsum(freq) / RATE
    tone = np.zeros(n)
    for k, a in [(1, 1.0), (2, 0.55), (3, 0.42), (4, 0.22), (5, 0.16), (6, 0.08), (7, 0.05)]:
        tone += a * np.sin(k * phase + r.uniform(0, 6.28))
    # aspereza: modulação de amplitude rápida e irregular (trepidação da borracha)
    rough = 1 + 0.45 * smooth_noise(n, 90, seed + 2) + 0.25 * np.sin(2 * np.pi * 63 * t)
    rough = np.clip(rough, 0.15, None)
    # chiado da borracha no asfalto
    hiss = band(r.standard_normal(n), 1500, 7500) * 0.35
    hiss *= 1 + 0.6 * smooth_noise(n, 25, seed + 3)
    # ronco da rodagem
    rumble = band(r.standard_normal(n), 40, 220) * 0.5 * (1 - prog) ** 0.5
    # envelope: entrada rápida, sustenta, e some quando o carro para
    env = np.clip(t / 0.06, 0, 1) * np.clip((1 - prog) / 0.12, 0, 1) ** 0.7
    env *= 0.85 + 0.15 * smooth_noise(n, 3, seed + 4)
    x = (tone * rough * 0.7 + hiss) * env + rumble * np.clip(t / 0.1, 0, 1)
    x = np.tanh(x * 1.8)
    if stop_bump:
        # leve "tranco" da suspensão quando o carro para
        bt = t_axis(0.5)
        bump = band(r.standard_normal(len(bt)), 30, 160) * np.exp(-bt * 9) * 0.9
        x = np.concatenate([x, np.zeros(len(bt) // 2)])
        s = n - int(RATE * 0.05)
        x[s:s + len(bump)] += bump[:len(x) - s]
    return finish(reverb(x, 0.5, 0.12))


def main(out):
    out = pathlib.Path(out)
    out.mkdir(parents=True, exist_ok=True)
    sounds = {
        'campainha-ding-dong.wav': campainha_ding_dong(),
        'campainha-eletrica.wav': campainha_eletrica(),
        'freio-de-carro.wav': freada(2.2, 1180, 0.16, seed=11),
        'freio-de-carro-longo.wav': freada(3.8, 1050, 0.24, seed=29),
    }
    for name, x in sounds.items():
        save(out / name, x)
        print(f'{name}: {len(x) / RATE:.2f}s')


if __name__ == '__main__':
    main(sys.argv[1] if len(sys.argv) > 1 else 'sons')

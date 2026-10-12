# Restore original JPEG bytes for production padding (the research fresh set already has padding).
import tarfile,pathlib,random,json,collections
root=pathlib.Path('/tmp/cineinsight-apple-validation');out=root/'fresh-original';out.mkdir(exist_ok=True);rng=random.Random(20261012)
with tarfile.open(root/'lfw.tgz','r:gz') as archive:
    groups=collections.defaultdict(list)
    for member in archive.getmembers():
        if member.isfile() and member.name.endswith('.jpg'):
            groups[pathlib.PurePosixPath(member.name).parts[-2]].append(member)
    subjects=sorted(name for name,items in groups.items() if len(items)>=10)
    rng.shuffle(subjects);records=[]
    for person,name in enumerate(subjects[100:140]):
        for shot,member in enumerate(rng.sample(sorted(groups[name],key=lambda m:m.name),10)):
            path=out/f'fresh-{person:02d}-{shot:02d}.jpg'
            path.write_bytes(archive.extractfile(member).read())
            records.append(dict(id=path.stem,path=str(path),person=person+100,shot=shot,dataset='fresh'))
    (root/'fresh-original-manifest.json').write_text(json.dumps(records))

import json, os, pathlib, subprocess, tempfile, unittest

BUILDER = pathlib.Path(__file__).resolve().parents[2] / 'scripts/build-image.sh'
MOCK = r'''#!/usr/bin/python3
import json,os,sys
from pathlib import Path
n=Path(sys.argv[0]).name;a=sys.argv[1:];root=Path(os.environ['MOCK_ROOT']);case=os.environ['MOCK_CASE']
with (root/'events').open('a') as f:f.write(json.dumps([n,*a])+'\n')
if n=='curl':
 p=Path(a[a.index('-o')+1]);p.write_bytes(b'mock');(root/'work').write_text(str(p.parent))
elif n=='sha256sum':
 if case=='bad_base':sys.exit(37)
elif n=='xz':Path(a[-1][:-3]).write_bytes(b'image')
elif n=='losetup' and '-f' in a:print('/dev/loop-test')
elif n=='partprobe' and case=='partprobe':sys.exit(37)
elif n=='parted':(root/'parted').touch()
elif n=='blockdev':print(2048 if (root/'parted').exists() and case!='no_partition_growth' else 1024)
elif n=='e2fsck':sys.exit(int(os.environ['FS_STATUS']))
elif n=='dumpe2fs':print('Block count: '+('200' if (root/'resized').exists() and case!='no_fs_growth' else '100'))
elif n=='resize2fs':(root/'resized').touch()
elif n=='install' and case=='after_bootmount':sys.exit(37)
elif n=='umount' and case=='cleanup_failure':sys.exit(37)
elif n=='mount':
 target=Path(a[-1]);isroot=target.name=='root'
 if (case=='rootmount' and isroot) or (case=='bootmount' and not isroot):sys.exit(37)
 if isroot:
  for p in ['etc/systemd/system','usr/local/bin']: (target/p).mkdir(parents=True,exist_ok=True)
'''

class BuilderMocks(unittest.TestCase):
 def run_case(self,case='ok',fs=0):
  with tempfile.TemporaryDirectory() as t:
   root=pathlib.Path(t);bin=root/'bin';bin.mkdir();mock=bin/'mock';mock.write_text(MOCK);mock.chmod(0o755)
   for n in ['curl','sha256sum','xz','dd','losetup','partprobe','parted','blockdev','e2fsck','dumpe2fs','resize2fs','mount','umount','install','chown','sleep']:(bin/n).symlink_to(mock)
   fake=root/'binary';fake.write_text('synthetic');env={'PATH':str(bin)+':/usr/bin:/bin','MOCK_ROOT':str(root),'MOCK_CASE':case,'FS_STATUS':str(fs),'TMPDIR':str(root)}
   r=subprocess.run(['bash',str(BUILDER),str(fake),'arm64',str(root/'out.img'),'v0.15.0'],env=env,capture_output=True,text=True)
   events=[json.loads(x) for x in (root/'events').read_text().splitlines()];work=pathlib.Path((root/'work').read_text());self.assertEqual(work.exists(),case=='cleanup_failure','unexpected work directory state')
   return r,events
 def test_cleanup_after_failures(self):
  for case in ['bad_base','partprobe','rootmount','bootmount','after_bootmount']:
   with self.subTest(case=case):
    r,e=self.run_case(case);self.assertEqual(r.returncode,37);names=[x[0] for x in e]
    self.assertEqual('losetup' in names,case!='bad_base')
    self.assertEqual(any(x[0]=='losetup' and x[1:]==['-d','/dev/loop-test'] for x in e),case!='bad_base')
    um=[x[-1].split('/')[-1] for x in e if x[0]=='umount'];self.assertEqual(um,['boot','root'] if case=='after_bootmount' else (['root'] if case=='bootmount' else []))
 def test_filesystem_status(self):
  for code in [0,1,2,4,8]:
   with self.subTest(code=code):
    r,e=self.run_case(fs=code);self.assertEqual(r.returncode,0 if code in [0,1] else code)
    names=[x[0] for x in e];self.assertEqual('resize2fs' in names,code in [0,1])
    self.assertTrue(any(x[0]=='losetup' and x[1:]==['-d','/dev/loop-test'] for x in e))
    if code in [0,1]:self.assertEqual([x[-1].split('/')[-1] for x in e if x[0]=='umount'],['boot','root'])
 def test_cleanup_failure_blocks_success(self):
  r,e=self.run_case('cleanup_failure');self.assertEqual(r.returncode,1);self.assertTrue(any(x[0]=='losetup' and x[1:]==['-d','/dev/loop-test'] for x in e))
 def test_no_growth_fails_closed(self):
  for case in ['no_partition_growth','no_fs_growth']:
   with self.subTest(case=case):
    r,e=self.run_case(case);self.assertNotEqual(r.returncode,0);self.assertTrue(any(x[0]=='losetup' and x[1:]==['-d','/dev/loop-test'] for x in e));self.assertFalse(any(x[0]=='mount' for x in e))
 def test_partition_growth_precedes_filesystem_resize(self):
  r,e=self.run_case();self.assertEqual(r.returncode,0);names=[x[0] for x in e]
  self.assertLess(names.index('parted'),names.index('e2fsck'));self.assertLess(names.index('e2fsck'),names.index('resize2fs'));self.assertLess(names.index('resize2fs'),names.index('mount'))
  self.assertEqual(sum(n=='partprobe' for n in names),2)
if __name__=='__main__':unittest.main(verbosity=2)

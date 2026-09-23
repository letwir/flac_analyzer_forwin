from unittest.mock import patch, call, Mock
import torch
import worker_gpu_daemon

def test_handle_cleanup_gpu():
    with patch('worker_gpu_daemon.gc.collect') as mock_gc, \
         patch('worker_gpu_daemon.torch.cuda.is_available', return_value=True) as mock_is_avail, \
         patch('worker_gpu_daemon.torch.cuda.synchronize') as mock_sync, \
         patch('worker_gpu_daemon.torch.cuda.empty_cache') as mock_empty_cache:

        device = torch.device('cuda')

        manager = Mock()
        manager.attach_mock(mock_gc, 'gc')
        manager.attach_mock(mock_sync, 'sync')
        manager.attach_mock(mock_empty_cache, 'empty_cache')

        res = worker_gpu_daemon.handle_cleanup_gpu(device)

        assert res["status"] == "success"

        manager.assert_has_calls([
            call.gc(),
            call.sync(device),
            call.empty_cache()
        ])

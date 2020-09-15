<?php
function jump($u,$q) {
    $rq=http_build_query($q);
	header('Location: '.$u.'?'.$rq);
}
function get($u,$token) {
    $opts = array(
        'http' => array(
            'method' => 'GET',
            'header' => array('Authorization: token '.$token,'User-Agent: PHP'),
            'timeout' => 15 * 60 // 超时时间（单位:s）
        ),
		'ssl' => array(
            'verify_peer'=>false,
            'verify_peer_name'=>false,
        )
    );
    $ct = stream_context_create($opts);
    $result = file_get_contents($u, false, $ct);
    return $result;
}
function post($u,$q) {
    $opts = array(
        'http' => array(
            'method' => 'POST',
            'header' => 'Content-type:application/x-www-form-urlencoded',
            'timeout' => 15 * 60, // 超时时间（单位:s）
			'content'=>http_build_query($q)
        ),
		'ssl' => array(
            'verify_peer'=>false,
            'verify_peer_name'=>false,
        )
    );
    $ct = stream_context_create($opts);
    $result = file_get_contents($u, false, $ct);
    return $result;
}
?>